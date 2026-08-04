package api

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/config"
)

func TestSMTPRequiresSTARTTLSOutsideDevelopment(t *testing.T) {
	address, commands := startPlainSMTPServer(t)
	sender := &smtpSender{config: config.SMTP{Address: address, Username: "user", FromAddress: "sender@example.test"}, password: "password"}
	if err := sender.Send(context.Background(), "recipient@example.test", "Subject", "Body"); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("plaintext production SMTP error=%v", err)
	}
	if strings.Contains(commands(), "MAIL FROM") {
		t.Fatal("production sender transmitted an envelope before STARTTLS")
	}
}

func TestSMTPDevelopmentDeliveryAndHeaderSanitation(t *testing.T) {
	address, commands := startPlainSMTPServer(t)
	sender := &smtpSender{config: config.SMTP{Address: address, FromAddress: "sender@example.test", AllowInsecureLocal: true}}
	if err := sender.Send(context.Background(), "recipient@example.test", "Subject", "Body"); err != nil {
		t.Fatal(err)
	}
	if transcript := commands(); !strings.Contains(transcript, "MAIL FROM:<sender@example.test>") || !strings.Contains(transcript, "Subject: Subject") {
		t.Fatalf("development SMTP transcript did not contain expected envelope/message: %q", transcript)
	}
	if err := sender.Send(context.Background(), "recipient@example.test\r\nBcc: victim@example.test", "Subject", "Body"); err == nil {
		t.Fatal("SMTP recipient header injection was accepted")
	}
}

func TestDeliveryQueueFullIsBoundedAndRecorded(t *testing.T) {
	service := &deliveryService{queue: make(chan deliveryWork, 32)}
	for range 32 {
		service.queue <- deliveryWork{}
	}
	category := ""
	if service.enqueue(deliveryWork{done: func(value string) bool { category = value; return true }}) {
		t.Fatal("full recovery queue accepted more work")
	}
	if category != "queue_full" {
		t.Fatalf("queue-full category=%q", category)
	}
}

func startPlainSMTPServer(t *testing.T) (string, func() string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var mu sync.Mutex
	var transcript strings.Builder
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		_, _ = connection.Write([]byte("220 mailpit.test ESMTP\r\n"))
		reader := bufio.NewReader(connection)
		inData := false
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}
			mu.Lock()
			transcript.WriteString(line)
			mu.Unlock()
			trimmed := strings.TrimSpace(line)
			if inData {
				if trimmed == "." {
					inData = false
					_, _ = connection.Write([]byte("250 queued\r\n"))
				}
				continue
			}
			switch {
			case strings.HasPrefix(trimmed, "EHLO"):
				_, _ = connection.Write([]byte("250-mailpit.test\r\n250 OK\r\n"))
			case strings.HasPrefix(trimmed, "HELO"), strings.HasPrefix(trimmed, "MAIL FROM"), strings.HasPrefix(trimmed, "RCPT TO"):
				_, _ = connection.Write([]byte("250 OK\r\n"))
			case trimmed == "DATA":
				inData = true
				_, _ = connection.Write([]byte("354 End data\r\n"))
			case trimmed == "QUIT":
				_, _ = connection.Write([]byte("221 Bye\r\n"))
				return
			default:
				_, _ = connection.Write([]byte("250 OK\r\n"))
			}
		}
	}()
	return listener.Addr().String(), func() string { mu.Lock(); defer mu.Unlock(); return transcript.String() }
}
