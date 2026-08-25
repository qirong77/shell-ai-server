package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	serviceTypes    = map[string]bool{"web": true, "api": true, "database": true, "cache": true, "queue": true, "worker": true, "proxy": true, "custom": true}
	serviceStatuses = map[string]bool{"running": true, "stopped": true, "error": true, "deploying": true, "unknown": true}
)

// HealthCheck config for a registered service.
type HealthCheck struct {
	Type     string  `json:"type"`
	URL      *string `json:"url"`
	Interval int     `json:"interval"`
}

// ServiceRecord is the standard record stored in data/services.json.
type ServiceRecord struct {
	ID           string                 `json:"id"`
	Name         string                 `json:"name"`
	Type         string                 `json:"type"`
	Port         *int                   `json:"port"`
	PID          *int                   `json:"pid"`
	Host         string                 `json:"host"`
	Status       string                 `json:"status"`
	HealthCheck  *HealthCheck           `json:"healthCheck"`
	Tags         []string               `json:"tags"`
	Config       map[string]interface{} `json:"config"`
	Metadata     map[string]interface{} `json:"metadata"`
	WorkingDir   *string                `json:"workingDir"`
	StartCommand *string                `json:"startCommand"`
	StopCommand  *string                `json:"stopCommand"`
	LogPath      *string                `json:"logPath"`
	DeployedAt   string                 `json:"deployedAt"`
	UpdatedAt    string                 `json:"updatedAt"`
}

// NewServiceRecord builds a record from raw input, applying defaults and
// validating enumerated fields.
func NewServiceRecord(rec map[string]interface{}) *ServiceRecord {
	now := time.Now().UTC().Format(time.RFC3339)
	id := stringField(rec, "id")
	if id == "" {
		id = randomUUID()
	}
	name := stringField(rec, "name")
	if name == "" {
		name = "unnamed-service"
	}
	sr := &ServiceRecord{
		ID:         id,
		Name:       name,
		Type:       "custom",
		Host:       "localhost",
		Status:     "unknown",
		Tags:       []string{},
		Config:     map[string]interface{}{},
		Metadata:   map[string]interface{}{},
		DeployedAt: now,
		UpdatedAt:  now,
	}
	if t := stringField(rec, "type"); t != "" && serviceTypes[t] {
		sr.Type = t
	}
	if p := intField(rec, "port"); p != nil {
		sr.Port = p
	}
	if p := intField(rec, "pid"); p != nil {
		sr.PID = p
	}
	if h := stringField(rec, "host"); h != "" {
		sr.Host = h
	}
	if s := stringField(rec, "status"); s != "" && serviceStatuses[s] {
		sr.Status = s
	}
	if hc, ok := rec["healthCheck"].(map[string]interface{}); ok {
		interval := 30
		if v, ok2 := hc["interval"]; ok2 {
			interval = boundedInt(v, 30, 1, 86400)
		}
		hcObj := &HealthCheck{Type: "none", Interval: interval}
		if t := stringField(hc, "type"); t != "" {
			hcObj.Type = t
		}
		if u := stringField(hc, "url"); u != "" {
			hcObj.URL = &u
		}
		sr.HealthCheck = hcObj
	}
	if tags, ok := rec["tags"].([]interface{}); ok {
		for _, t := range tags {
			if s, ok2 := t.(string); ok2 {
				sr.Tags = append(sr.Tags, s)
			}
		}
	}
	if c, ok := rec["config"].(map[string]interface{}); ok {
		sr.Config = c
	}
	if m, ok := rec["metadata"].(map[string]interface{}); ok {
		sr.Metadata = m
	}
	sr.WorkingDir = stringPtrField(rec, "workingDir")
	sr.StartCommand = stringPtrField(rec, "startCommand")
	sr.StopCommand = stringPtrField(rec, "stopCommand")
	sr.LogPath = stringPtrField(rec, "logPath")
	if d := stringField(rec, "deployedAt"); d != "" {
		sr.DeployedAt = d
	}
	return sr
}

// ServiceRegistry is a persistent JSON store of service records.
type ServiceRegistry struct {
	mu       sync.RWMutex
	filePath string
	services map[string]*ServiceRecord
}

func NewServiceRegistry(filePath string) *ServiceRegistry {
	reg := &ServiceRegistry{
		filePath: filePath,
		services: map[string]*ServiceRecord{},
	}
	reg.load()
	return reg
}

func (reg *ServiceRegistry) load() {
	data, err := os.ReadFile(reg.filePath)
	if err != nil {
		return
	}
	var arr []*ServiceRecord
	if err := json.Unmarshal(data, &arr); err != nil {
		// Rename corrupt file aside.
		corrupt := reg.filePath + ".corrupt-" + time.Now().Format("20060102150405")
		os.Rename(reg.filePath, corrupt)
		return
	}
	for _, s := range arr {
		if s != nil {
			reg.services[s.ID] = s
		}
	}
}

func (reg *ServiceRegistry) persist() {
	reg.mu.RLock()
	records := make([]*ServiceRecord, 0, len(reg.services))
	for _, s := range reg.services {
		records = append(records, s)
	}
	reg.mu.RUnlock()

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return
	}
	tmp := reg.filePath + "." + itoa(os.Getpid()) + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, reg.filePath)
}

// Register adds a service and persists. It returns the created record.
func (reg *ServiceRegistry) Register(rec map[string]interface{}) *ServiceRecord {
	sr := NewServiceRecord(rec)
	reg.mu.Lock()
	reg.services[sr.ID] = sr
	reg.mu.Unlock()
	reg.persist()
	return sr
}

// Query returns services matching the given filters.
func (reg *ServiceRegistry) Query(filters map[string]interface{}) []*ServiceRecord {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	results := make([]*ServiceRecord, 0, len(reg.services))
	for _, s := range reg.services {
		results = append(results, s)
	}

	// Apply filters.
	if q, ok := filters["q"].(string); ok && q != "" {
		ql := toLower(q)
		filtered := results[:0]
		for _, s := range results {
			if contains(toLower(s.Name), ql) ||
				containsTag(s.Tags, ql) ||
				contains(toLower(anyString(s.Metadata)), ql) {
				filtered = append(filtered, s)
			}
		}
		results = filtered
	}
	if v, ok := filters["name"].(string); ok && v != "" {
		results = filterBySlice(results, func(s *ServiceRecord) bool { return s.Name == v })
	}
	if v, ok := filters["type"].(string); ok && v != "" {
		results = filterBySlice(results, func(s *ServiceRecord) bool { return s.Type == v })
	}
	if v, ok := filters["status"].(string); ok && v != "" {
		results = filterBySlice(results, func(s *ServiceRecord) bool { return s.Status == v })
	}
	if v, ok := filters["tag"].(string); ok && v != "" {
		results = filterBySlice(results, func(s *ServiceRecord) bool { return containsTag(s.Tags, v) })
	}
	if v, ok := filters["port"]; ok && v != nil {
		port := toInt(v)
		results = filterBySlice(results, func(s *ServiceRecord) bool { return s.Port != nil && *s.Port == port })
	}
	if v, ok := filters["pid"]; ok && v != nil {
		pid := toInt(v)
		results = filterBySlice(results, func(s *ServiceRecord) bool { return s.PID != nil && *s.PID == pid })
	}
	if v, ok := filters["host"].(string); ok && v != "" {
		results = filterBySlice(results, func(s *ServiceRecord) bool { return s.Host == v })
	}
	return results
}

func (reg *ServiceRegistry) Get(id string) *ServiceRecord {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return reg.services[id]
}

func (reg *ServiceRegistry) Update(id string, patch map[string]interface{}) *ServiceRecord {
	reg.mu.Lock()
	existing, ok := reg.services[id]
	if !ok {
		reg.mu.Unlock()
		return nil
	}
	// Merge scalar fields from patch.
	merged := *existing
	if v, ok := patch["name"].(string); ok && v != "" {
		merged.Name = v
	}
	if v, ok := patch["type"].(string); ok && serviceTypes[v] {
		merged.Type = v
	}
	if v, ok := patch["host"].(string); ok && v != "" {
		merged.Host = v
	}
	if v, ok := patch["status"].(string); ok && serviceStatuses[v] {
		merged.Status = v
	}
	if _, ok := patch["port"]; ok {
		if p := intField(patch, "port"); p != nil {
			merged.Port = p
		}
	}
	if _, ok := patch["pid"]; ok {
		if p := intField(patch, "pid"); p != nil {
			merged.PID = p
		}
	}
	if v, ok := patch["workingDir"]; ok {
		if s, ok2 := v.(string); ok2 {
			merged.WorkingDir = &s
		} else {
			merged.WorkingDir = nil
		}
	}
	if v, ok := patch["startCommand"]; ok {
		if s, ok2 := v.(string); ok2 {
			merged.StartCommand = &s
		} else {
			merged.StartCommand = nil
		}
	}
	if v, ok := patch["stopCommand"]; ok {
		if s, ok2 := v.(string); ok2 {
			merged.StopCommand = &s
		} else {
			merged.StopCommand = nil
		}
	}
	if v, ok := patch["logPath"]; ok {
		if s, ok2 := v.(string); ok2 {
			merged.LogPath = &s
		} else {
			merged.LogPath = nil
		}
	}
	if tags, ok := patch["tags"].([]interface{}); ok {
		merged.Tags = []string{}
		for _, t := range tags {
			if s, ok2 := t.(string); ok2 {
				merged.Tags = append(merged.Tags, s)
			}
		}
	}
	if c, ok := patch["config"].(map[string]interface{}); ok && c != nil {
		if merged.Config == nil {
			merged.Config = map[string]interface{}{}
		}
		for k, v := range c {
			merged.Config[k] = v
		}
	}
	if m, ok := patch["metadata"].(map[string]interface{}); ok && m != nil {
		if merged.Metadata == nil {
			merged.Metadata = map[string]interface{}{}
		}
		for k, v := range m {
			merged.Metadata[k] = v
		}
	}
	if hc, ok := patch["healthCheck"].(map[string]interface{}); ok && hc != nil {
		if merged.HealthCheck == nil {
			merged.HealthCheck = &HealthCheck{Type: "none", Interval: 30}
		}
		if t := stringField(hc, "type"); t != "" {
			merged.HealthCheck.Type = t
		}
		if v, ok2 := hc["interval"]; ok2 {
			merged.HealthCheck.Interval = boundedInt(v, merged.HealthCheck.Interval, 1, 86400)
		}
		if u := stringField(hc, "url"); u != "" {
			merged.HealthCheck.URL = &u
		}
	}
	merged.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	reg.services[id] = &merged
	reg.mu.Unlock()
	reg.persist()
	copy := merged
	return &copy
}

func (reg *ServiceRegistry) Delete(id string) bool {
	reg.mu.Lock()
	existed := false
	if _, ok := reg.services[id]; ok {
		delete(reg.services, id)
		existed = true
	}
	reg.mu.Unlock()
	if existed {
		reg.persist()
	}
	return existed
}

func (reg *ServiceRegistry) Clear() int {
	reg.mu.Lock()
	n := len(reg.services)
	reg.services = map[string]*ServiceRecord{}
	reg.mu.Unlock()
	reg.persist()
	return n
}

func (reg *ServiceRegistry) Size() int {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return len(reg.services)
}

// ---------------------------------------------------------------------------
// field/conversion helpers

func filterBySlice(in []*ServiceRecord, pred func(*ServiceRecord) bool) []*ServiceRecord {
	out := in[:0]
	for _, s := range in {
		if pred(s) {
			out = append(out, s)
		}
	}
	return out
}

func stringField(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func intField(m map[string]interface{}, key string) *int {
	if v, ok := m[key]; ok && v != nil {
		if f, ok2 := v.(float64); ok2 {
			i := int(f)
			return &i
		}
		if s, ok2 := v.(string); ok2 {
			i, err := parseInt(s)
			if err == nil {
				return &i
			}
		}
	}
	return nil
}

func stringPtrField(m map[string]interface{}, key string) *string {
	if v, ok := m[key].(string); ok && v != "" {
		p := v
		return &p
	}
	return nil
}

func randomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "uuid-" + itoa64(time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func toLower(s string) string {
	return strings.ToLower(s)
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func containsTag(tags []string, sub string) bool {
	for _, t := range tags {
		if contains(t, sub) {
			return true
		}
	}
	return false
}

func anyString(v interface{}) string {
	if v == nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func toInt(v interface{}) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	if s, ok := v.(string); ok {
		if i, err := parseInt(s); err == nil {
			return i
		}
	}
	return 0
}

func parseInt(s string) (int, error) {
	var n int
	if s == "" {
		return 0, os.ErrInvalid
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	}
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, os.ErrInvalid
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

var _ = strings.Contains
