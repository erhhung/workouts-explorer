package api

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"github.com/erhhung/workouts-explorer/internal/config"
)

type mailSender interface {
	Send(context.Context, string, string, string) error
}

type smtpSender struct {
	config   config.SMTP
	password string
}

func newSMTPSender(cfg config.SMTP) (mailSender, error) {
	if cfg.Address == "" {
		return unavailableMailSender{}, nil
	}
	password := ""
	if cfg.PasswordFile != "" {
		value, err := readPrivateRegularFile(cfg.PasswordFile, 4096)
		if err != nil || len(value) == 0 || len(value) > 4096 || strings.ContainsRune(string(value), 0) {
			return nil, errors.New("SMTP password file is invalid")
		}
		password = string(value)
	}
	return &smtpSender{config: cfg, password: password}, nil
}

func (s *smtpSender) Send(ctx context.Context, recipient, subject, body string) error {
	if strings.ContainsAny(recipient, "\r\n") || strings.ContainsAny(subject, "\r\n") || strings.ContainsAny(s.config.FromAddress, "\r\n") {
		return errors.New("SMTP message headers are invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	host, _, _ := net.SplitHostPort(s.config.Address)
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", s.config.Address)
	if err != nil {
		return err
	}
	defer connection.Close()
	deadline, _ := ctx.Deadline()
	_ = connection.SetDeadline(deadline)
	client, err := smtp.NewClient(connection, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if !s.config.AllowInsecureLocal {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP STARTTLS unavailable")
		}
		if err := client.StartTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}); err != nil {
			return err
		}
		if err := client.Auth(smtp.PlainAuth("", s.config.Username, s.password, host)); err != nil {
			return err
		}
	}
	if err := client.Mail(s.config.FromAddress); err != nil {
		return err
	}
	if err := client.Rcpt(recipient); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message := "From: " + s.config.FromAddress + "\r\nTo: " + recipient + "\r\nSubject: " + subject +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + body
	if _, err := writer.Write([]byte(message)); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

type unavailableMailSender struct{}

func (unavailableMailSender) Send(context.Context, string, string, string) error {
	return errors.New("mail service unavailable")
}

type deliveryWork struct {
	created time.Time
	to      string
	subject string
	body    string
	done    func(string) bool
	result  chan string
}

type deliveryService struct {
	ctx    context.Context
	cancel context.CancelFunc
	sender mailSender
	queue  chan deliveryWork
	wg     sync.WaitGroup
}

func newDeliveryService(sender mailSender) *deliveryService {
	ctx, cancel := context.WithCancel(context.Background())
	service := &deliveryService{ctx: ctx, cancel: cancel, sender: sender, queue: make(chan deliveryWork, 32)}
	for range 2 {
		service.wg.Add(1)
		go service.worker()
	}
	return service
}

func (s *deliveryService) close() {
	s.cancel()
	s.wg.Wait()
	for {
		select {
		case work := <-s.queue:
			if work.done != nil {
				work.done("interrupted")
			}
			if work.result != nil {
				work.result <- "interrupted"
			}
		default:
			return
		}
	}
}

func (s *deliveryService) enqueue(work deliveryWork) bool {
	work.created = time.Now()
	select {
	case s.queue <- work:
		return true
	default:
		if work.done != nil {
			_ = work.done("queue_full")
		}
		return false
	}
}

func (s *deliveryService) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case work := <-s.queue:
			category := ""
			if time.Since(work.created) > 30*time.Second {
				category = "interrupted"
			} else if err := s.sender.Send(s.ctx, work.to, work.subject, work.body); err != nil {
				category = smtpCategory(err)
			}
			if work.done != nil {
				if !work.done(category) && category == "" {
					category = "interrupted"
				}
			}
			if work.result != nil {
				work.result <- category
			}
		}
	}
}

func smtpCategory(err error) string {
	var networkError net.Error
	var protocolError *textproto.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &networkError) && networkError.Timeout():
		return "timeout"
	case errors.As(err, &protocolError) && protocolError.Code == 535:
		return "authentication"
	case errors.As(err, &protocolError):
		return "rejected"
	case strings.Contains(strings.ToLower(err.Error()), "tls"):
		return "tls"
	default:
		return "interrupted"
	}
}
