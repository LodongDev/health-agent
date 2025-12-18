package resolver

import (
	"regexp"
	"strings"

	"docker-health-agent/internal/types"
)

// 이미지 패턴
var imagePatterns = []struct {
	pattern    *regexp.Regexp
	typ        string
	subtype    string
	confidence int
}{
	// DB
	{regexp.MustCompile(`(?i)mysql|mariadb`), "db", "mysql", 95},
	{regexp.MustCompile(`(?i)postgres`), "db", "postgres", 95},
	{regexp.MustCompile(`(?i)mongo`), "db", "mongodb", 95},
	{regexp.MustCompile(`(?i)mssql|sqlserver`), "db", "mssql", 95},
	{regexp.MustCompile(`(?i)oracle`), "db", "oracle", 95},

	// Cache
	{regexp.MustCompile(`(?i)redis`), "cache", "redis", 95},
	{regexp.MustCompile(`(?i)memcached`), "cache", "memcached", 95},

	// Proxy
	{regexp.MustCompile(`(?i)nginx`), "proxy", "nginx", 85},
	{regexp.MustCompile(`(?i)httpd|apache`), "proxy", "apache", 85},
	{regexp.MustCompile(`(?i)traefik`), "proxy", "traefik", 90},
	{regexp.MustCompile(`(?i)caddy`), "proxy", "caddy", 85},
	{regexp.MustCompile(`(?i)haproxy`), "proxy", "haproxy", 85},

	// Worker/Queue
	{regexp.MustCompile(`(?i)rabbitmq`), "worker", "rabbitmq", 85},
	{regexp.MustCompile(`(?i)kafka`), "worker", "kafka", 85},
	{regexp.MustCompile(`(?i)celery`), "worker", "celery", 80},

	// API (런타임)
	{regexp.MustCompile(`(?i)openjdk|java|spring`), "api", "java", 60},
	{regexp.MustCompile(`(?i)node`), "api", "node", 50},
	{regexp.MustCompile(`(?i)python|django|flask|fastapi`), "api", "python", 55},
	{regexp.MustCompile(`(?i)golang|go:`), "api", "go", 55},
	{regexp.MustCompile(`(?i)dotnet|aspnet`), "api", "dotnet", 60},
}

// 포트 매핑
var portMappings = map[int]struct {
	typ        string
	subtype    string
	confidence int
}{
	3306:  {"db", "mysql", 90},
	5432:  {"db", "postgres", 90},
	27017: {"db", "mongodb", 90},
	1433:  {"db", "mssql", 90},
	1521:  {"db", "oracle", 90},
	6379:  {"cache", "redis", 90},
	11211: {"cache", "memcached", 90},
	80:    {"web", "", 40},
	443:   {"web", "", 40},
	8080:  {"api", "", 35},
	3000:  {"api", "node", 35},
	5000:  {"api", "python", 35},
}

// Resolver 타입 판별기
type Resolver struct {
	labelPrefix string
}

// New Resolver 생성
func New(labelPrefix string) *Resolver {
	return &Resolver{labelPrefix: labelPrefix}
}

// Resolve 컨테이너 타입 판별
func (r *Resolver) Resolve(c types.ContainerInfo) types.ContainerType {
	// 1. Label 기반 (최우선)
	if t := r.fromLabel(c); t != nil {
		return *t
	}

	// 2. Image 기반
	if t := r.fromImage(c); t != nil {
		return *t
	}

	// 3. Port 기반
	if t := r.fromPort(c); t != nil {
		return *t
	}

	// 4. Unknown
	return types.ContainerType{
		Type:       "unknown",
		Confidence: 0,
		Source:     "default",
	}
}

func (r *Resolver) fromLabel(c types.ContainerInfo) *types.ContainerType {
	typeVal := c.Labels[r.labelPrefix+".type"]
	if typeVal == "" {
		return nil
	}

	if !isValidType(typeVal) {
		return nil
	}

	return &types.ContainerType{
		Type:       typeVal,
		Subtype:    c.Labels[r.labelPrefix+".subtype"],
		Confidence: 100,
		Source:     "label",
	}
}

func (r *Resolver) fromImage(c types.ContainerInfo) *types.ContainerType {
	image := strings.ToLower(c.Image)

	for _, p := range imagePatterns {
		if p.pattern.MatchString(image) {
			return &types.ContainerType{
				Type:       p.typ,
				Subtype:    p.subtype,
				Confidence: p.confidence,
				Source:     "image",
			}
		}
	}
	return nil
}

func (r *Resolver) fromPort(c types.ContainerInfo) *types.ContainerType {
	var best *types.ContainerType
	bestConfidence := 0

	for _, port := range c.Ports {
		if mapping, ok := portMappings[port.Private]; ok {
			if mapping.confidence > bestConfidence {
				best = &types.ContainerType{
					Type:       mapping.typ,
					Subtype:    mapping.subtype,
					Confidence: mapping.confidence,
					Source:     "port",
				}
				bestConfidence = mapping.confidence
			}
		}
	}
	return best
}

func isValidType(t string) bool {
	switch t {
	case "api", "web", "db", "cache", "worker", "proxy":
		return true
	}
	return false
}
