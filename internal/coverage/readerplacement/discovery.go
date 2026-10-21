package readerplacement

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5"
)

type endpoint struct {
	key       string
	port      uint16
	addresses []string
	policies  []*tls.Config
}

type configuredHost struct {
	host     string
	port     uint16
	policies []*tls.Config
}

func configuredHosts(config *pgx.ConnConfig) []configuredHost {
	var hosts []configuredHost
	add := func(host string, port uint16, policy *tls.Config) {
		for i := range hosts {
			if normalizeHost(hosts[i].host) == normalizeHost(host) && hosts[i].port == port {
				hosts[i].policies = append(hosts[i].policies, policy)
				return
			}
		}
		hosts = append(hosts, configuredHost{host: host, port: port, policies: []*tls.Config{policy}})
	}
	add(config.Host, config.Port, config.TLSConfig)
	for _, fallback := range config.Fallbacks {
		add(fallback.Host, fallback.Port, fallback.TLSConfig)
	}
	return hosts
}

func hashIdentity(value any) string {
	var data []byte
	if identity, ok := value.(string); ok {
		data = []byte(identity)
	} else {
		data, _ = json.Marshal(value)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizeHost(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func clusterIdentity(config *pgx.ConnConfig) string {
	var hosts []string
	seen := make(map[string]bool)
	for _, host := range configuredHosts(config) {
		identity := net.JoinHostPort(normalizeHost(host.host), strconv.Itoa(int(host.port)))
		if !seen[identity] {
			seen[identity] = true
			hosts = append(hosts, identity)
		}
	}
	sort.Strings(hosts)
	return hashIdentity(struct {
		Hosts    []string
		Database string
	}{hosts, config.Database})
}

func (m *Manager) endpoints(ctx context.Context) ([]endpoint, error) {
	var endpoints []endpoint
	seen := make(map[string]bool)
	hosts := configuredHosts(m.config)
	for _, host := range hosts {
		if strings.HasPrefix(host.host, "/") {
			return nil, errors.New("OSM reader placement requires TCP endpoints")
		}
		labels := strings.Split(normalizeHost(host.host), ".")
		// Choose the identity scheme from configuration, never from DNS success.
		// A single Kubernetes service needs canonical pod SRV identities; an
		// explicit pod/generic hostname is one conservative logical endpoint.
		if len(hosts) == 1 && len(labels) >= 3 && labels[2] == "svc" {
			_, records, srvErr := m.lookupSRV(ctx, "postgresql", "tcp", normalizeHost(host.host))
			if srvErr != nil {
				return nil, sanitize(srvErr)
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if len(records) == 0 {
				return nil, osm.ErrMatcherReplicaNotReady
			}
			// SRV groups all IPv4/IPv6 addresses for a pod under one identity.
			for _, record := range records {
				target := normalizeHost(record.Target)
				if target == "" || record.Port == 0 {
					continue
				}
				addresses, err := m.addresses(ctx, target)
				if err != nil {
					return nil, err
				}
				key := hashIdentity(net.JoinHostPort(target, strconv.Itoa(int(record.Port))))
				if !seen[key] {
					seen[key] = true
					endpoints = append(endpoints, endpoint{key, record.Port, addresses, host.policies})
				}
			}
			continue
		}
		addresses, err := m.addresses(ctx, host.host)
		if err != nil {
			return nil, err
		}
		key := hashIdentity(net.JoinHostPort(normalizeHost(host.host), strconv.Itoa(int(host.port))))
		if !seen[key] {
			seen[key] = true
			endpoints = append(endpoints, endpoint{key, host.port, addresses, host.policies})
		}
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].key < endpoints[j].key })
	return endpoints, nil
}

func (m *Manager) addresses(ctx context.Context, host string) ([]string, error) {
	addresses, err := m.config.LookupFunc(ctx, host)
	if err != nil {
		return nil, sanitize(err)
	}
	seen := make(map[string]bool)
	var normalized []string
	for _, address := range addresses {
		ip := net.ParseIP(address)
		if ip == nil {
			continue
		}
		address = ip.String() // Also normalizes IPv4-mapped IPv6 aliases.
		if !seen[address] {
			seen[address] = true
			normalized = append(normalized, address)
		}
	}
	sort.Slice(normalized, func(i, j int) bool {
		v4i, v4j := net.ParseIP(normalized[i]).To4() != nil, net.ParseIP(normalized[j]).To4() != nil
		if v4i != v4j {
			return v4i
		}
		return normalized[i] < normalized[j]
	})
	return normalized, nil
}

type probeResult struct {
	endpoint endpoint
	heads    map[string]int64
}

func (m *Manager) discover(ctx context.Context) ([]probeResult, error) {
	for {
		m.mu.Lock()
		if !m.cachedAt.IsZero() && time.Since(m.cachedAt) < cacheLifetime {
			results := m.cache
			m.mu.Unlock()
			return results, nil
		}
		if wait := m.refreshing; wait != nil {
			m.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		wait := make(chan struct{})
		m.refreshing = wait
		m.mu.Unlock()
		results, err := m.refresh(ctx)
		m.mu.Lock()
		if err == nil {
			m.cache, m.cachedAt = results, time.Now()
		}
		m.refreshing = nil
		close(wait)
		m.mu.Unlock()
		return results, err
	}
}

func (m *Manager) refresh(ctx context.Context) ([]probeResult, error) {
	dnsCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	endpoints, err := m.endpoints(dnsCtx)
	cancel()
	if err != nil {
		return nil, sanitize(err)
	}
	var mu sync.Mutex
	var results []probeResult
	var fatal error
	failed := make(chan struct{})
	var wg sync.WaitGroup
	for _, endpoint := range endpoints {
		select {
		case m.probes <- struct{}{}:
		case <-failed:
			wg.Wait()
			return nil, sanitize(fatal)
		case <-ctx.Done():
			wg.Wait()
			if fatal != nil {
				return nil, sanitize(fatal)
			}
			return nil, ctx.Err()
		}
		connection := &jobConnection{manager: m, probe: true}
		m.mu.Lock()
		m.active[connection] = struct{}{}
		m.mu.Unlock()
		wg.Go(func() {
			probeCtx, cancel := context.WithTimeout(ctx, connectTimeout)
			defer cancel()
			heads, err := m.probe(probeCtx, endpoint, connection)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				results = append(results, probeResult{endpoint, heads})
			} else if permanent(err) {
				if fatal == nil {
					close(failed)
				}
				fatal = errors.Join(fatal, err)
			}
		})
	}
	wg.Wait()
	if fatal != nil {
		return nil, sanitize(fatal)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return results, nil
}

func (m *Manager) probe(ctx context.Context, endpoint endpoint, connection *jobConnection) (heads map[string]int64, err error) {
	// Track probe sockets too: a pgx timeout can leave asynchronous cleanup
	// running after logical Close, otherwise exceeding the two-probe bound.
	defer func() {
		if !connection.close() {
			heads = nil
			err = errors.Join(ErrCleanupUnconfirmed, sanitize(err))
		}
	}()
	conn, err := m.connect(ctx, endpoint, connection)
	if err != nil {
		return nil, err
	}
	connection.mu.Lock()
	connection.conn = conn
	connection.mu.Unlock()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, sanitize(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	result := conn.PgConn().ExecParams(ctx, "SELECT region_id,id FROM osm_catalog.generations WHERE state='active'", nil, nil, nil, nil).Read()
	if result.Err != nil {
		return nil, sanitize(result.Err)
	}
	heads = make(map[string]int64, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) != 2 {
			return nil, errors.New("invalid OSM generation head result")
		}
		generation, err := strconv.ParseInt(string(row[1]), 10, 64)
		if err != nil {
			return nil, errors.New("invalid OSM generation head result")
		}
		heads[string(row[0])] = generation
	}
	return heads, nil
}
