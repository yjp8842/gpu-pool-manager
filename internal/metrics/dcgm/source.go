package dcgm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yjp8842/gpu-pool-manager/internal/metrics"
)

// Endpoint는 dcgm-exporter 하나의 주소다. Node가 비어 있으면 메트릭의 Hostname 라벨로 노드를 정한다.
type Endpoint struct {
	Node string
	URL  string
}

// ParseEndpoints는 DCGM_ENDPOINTS 항목을 읽는다. 항목은 "http://10.0.0.5:9400/metrics" 또는
// "gke-node-1=http://10.0.0.5:9400/metrics" 꼴이다.
func ParseEndpoints(items []string) ([]Endpoint, error) {
	out := make([]Endpoint, 0, len(items))
	for _, item := range items {
		var ep Endpoint
		if node, rest, ok := strings.Cut(item, "="); ok && !strings.Contains(node, "/") {
			ep.Node, ep.URL = strings.TrimSpace(node), strings.TrimSpace(rest)
			if ep.Node == "" {
				return nil, fmt.Errorf("DCGM_ENDPOINTS %q: 노드 이름이 비었다", item)
			}
		} else {
			ep.URL = strings.TrimSpace(item)
		}
		u, err := url.Parse(ep.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("DCGM_ENDPOINTS %q: http(s) 주소가 아니다", item)
		}
		out = append(out, ep)
	}
	return out, nil
}

// Source는 dcgm-exporter 엔드포인트들을 읽는 MetricSource다. 여러 고루틴에서 써도 된다.
type Source struct {
	endpoints []Endpoint
	client    *http.Client
	now       func() time.Time
}

var _ metrics.MetricSource = (*Source)(nil)

// requestTimeout은 엔드포인트 하나를 읽는 데 쓰는 최대 시간이다. 수집 주기(기본 15초)보다 짧게 둔다.
const requestTimeout = 10 * time.Second

// maxBodyBytes는 응답 하나에서 읽는 최대 크기다. GPU 8장 노드의 출력도 수백 KB 정도다.
const maxBodyBytes = 32 << 20

// New는 endpoints를 읽는 Source를 만든다. client가 nil이면 요청마다 10초 제한이 있는 기본 클라이언트를 쓴다.
func New(endpoints []Endpoint, client *http.Client) *Source {
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	return &Source{endpoints: endpoints, client: client, now: time.Now}
}

// Fetch는 엔드포인트를 동시에 읽어 노드별 스냅샷을 돌려준다. 일부를 읽지 못하면 읽은 스냅샷과 함께
// 엔드포인트별 오류를 묶어 돌려준다. 읽지 못한 노드는 스냅샷이 없으므로 이번 회차에 미관측으로 세지 않는다.
func (s *Source) Fetch(ctx context.Context) ([]metrics.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	at := s.now()
	results := make([][]metrics.Snapshot, len(s.endpoints))
	errs := make([]error, len(s.endpoints))
	var wg sync.WaitGroup
	for i, ep := range s.endpoints {
		wg.Go(func() {
			snaps, err := s.fetchOne(ctx, ep, at)
			if err != nil {
				errs[i] = fmt.Errorf("dcgm %s: %w", ep.URL, err)
				return
			}
			results[i] = snaps
		})
	}
	wg.Wait()

	var out []metrics.Snapshot
	for _, r := range results {
		out = append(out, r...)
	}
	return out, errors.Join(errs...)
}

func (s *Source) fetchOne(ctx context.Context, ep Endpoint, at time.Time) ([]metrics.Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.URL, nil)
	if err != nil {
		return nil, err
	}
	// 텍스트 형식만 읽는다 (protobuf로 협상하지 않게).
	req.Header.Set("Accept", "text/plain")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	return Parse(io.LimitReader(resp.Body, maxBodyBytes), ep.Node, at)
}
