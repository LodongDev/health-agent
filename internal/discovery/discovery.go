package discovery

import (
	"context"
	"strings"
	"time"

	"docker-health-agent/internal/types"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// Discovery Docker 컨테이너 발견
type Discovery struct {
	client      *client.Client
	labelPrefix string
}

// New Discovery 생성
func New(dockerSock, labelPrefix string) (*Discovery, error) {
	cli, err := client.NewClientWithOpts(
		client.WithHost("unix://"+dockerSock),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, err
	}

	return &Discovery{
		client:      cli,
		labelPrefix: labelPrefix,
	}, nil
}

// Ping Docker 연결 확인
func (d *Discovery) Ping(ctx context.Context) error {
	_, err := d.client.Ping(ctx)
	return err
}

// Discover 실행 중인 컨테이너 조회
func (d *Discovery) Discover(ctx context.Context) ([]types.ContainerInfo, error) {
	containers, err := d.client.ContainerList(ctx, container.ListOptions{
		All: false, // running only
	})
	if err != nil {
		return nil, err
	}

	var result []types.ContainerInfo

	for _, c := range containers {
		// 모니터링 제외 체크
		if c.Labels[d.labelPrefix+".exclude"] == "true" {
			continue
		}

		info := types.ContainerInfo{
			ID:      c.ID,
			Name:    cleanName(c.Names),
			Image:   c.Image,
			Status:  c.Status,
			State:   c.State,
			Labels:  c.Labels,
			Created: time.Unix(c.Created, 0),
			Ports:   extractPorts(c.Ports),
		}

		// 상세 정보 조회
		inspect, err := d.client.ContainerInspect(ctx, c.ID)
		if err == nil {
			info.Networks = extractNetworks(inspect.NetworkSettings)
			if inspect.State.Health != nil {
				info.DockerHealth = &types.DockerHealth{
					Status:        inspect.State.Health.Status,
					FailingStreak: inspect.State.Health.FailingStreak,
				}
			}
		}

		result = append(result, info)
	}

	return result, nil
}

// Close 연결 종료
func (d *Discovery) Close() error {
	return d.client.Close()
}

func cleanName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

func extractPorts(ports []container.Port) []types.PortMapping {
	var result []types.PortMapping
	for _, p := range ports {
		result = append(result, types.PortMapping{
			Private:  int(p.PrivatePort),
			Public:   int(p.PublicPort),
			Protocol: p.Type,
			IP:       p.IP,
		})
	}
	return result
}

func extractNetworks(settings *container.NetworkSettings) []types.NetworkInfo {
	if settings == nil {
		return nil
	}

	var result []types.NetworkInfo
	for name, net := range settings.Networks {
		if net.IPAddress != "" {
			result = append(result, types.NetworkInfo{
				Name:    name,
				IP:      net.IPAddress,
				Gateway: net.Gateway,
			})
		}
	}
	return result
}
