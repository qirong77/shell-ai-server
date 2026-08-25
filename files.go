package main

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func handleFilesGet(w http.ResponseWriter, r *http.Request) {
	filePath := queryStr(r, "path")
	if filePath == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'path' query param", nil, nil))
		return
	}
	abs := resolvePath(filePath)
	info, err := os.Stat(abs)
	if err != nil {
		sendJSON(w, 500, fail("FILE_ERROR", "File operation failed: "+err.Error(), nil, nil))
		return
	}
	if info.IsDir() {
		entries, err := os.ReadDir(abs)
		if err != nil {
			sendJSON(w, 500, fail("FILE_ERROR", "File operation failed: "+err.Error(), nil, nil))
			return
		}
		list := make([]map[string]interface{}, 0, len(entries))
		for _, e := range entries {
			etype := "file"
			if e.IsDir() {
				etype = "dir"
			} else if e.Type()&os.ModeSymlink != 0 {
				etype = "symlink"
			}
			sizeVal := interface{}(nil)
			if fi, err := e.Info(); err == nil {
				sizeVal = fi.Size()
			}
			list = append(list, map[string]interface{}{"name": e.Name(), "type": etype, "size": sizeVal})
		}
		sendJSON(w, 200, ok(map[string]interface{}{"path": abs, "type": "dir", "entries": list},
			map[string]interface{}{"count": len(list)}))
		return
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		sendJSON(w, 500, fail("FILE_ERROR", "File operation failed: "+err.Error(), nil, nil))
		return
	}
	sendJSON(w, 200, ok(map[string]interface{}{
		"path": abs, "type": "file", "size": info.Size(), "content": string(content),
	}, map[string]interface{}{"size": info.Size()}))
}

func handleFilesWrite(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	obj, err := parseJSONBody(body)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	filePath := stringField(obj, "path")
	if filePath == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'path' field", nil, nil))
		return
	}
	if _, ok := obj["content"]; !ok {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'content' field", nil, nil))
		return
	}
	content := anyString(obj["content"])
	appendMode := false
	if v, ok := obj["append"].(bool); ok {
		appendMode = v
	}
	abs := resolvePath(filePath)
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		sendJSON(w, 500, fail("FILE_ERROR", "File write failed: "+err.Error(), nil, nil))
		return
	}
	var f *os.File
	if appendMode {
		f, err = os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	} else {
		f, err = os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	}
	if err != nil {
		sendJSON(w, 500, fail("FILE_ERROR", "File write failed: "+err.Error(), nil, nil))
		return
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		sendJSON(w, 500, fail("FILE_ERROR", "File write failed: "+err.Error(), nil, nil))
		return
	}
	info, _ := os.Stat(abs)
	sendJSON(w, 200, ok(map[string]interface{}{"path": abs, "size": info.Size(), "appended": appendMode}, nil))
}

func handleFilesMkdir(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	obj, err := parseJSONBody(body)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	dir := stringField(obj, "path")
	if dir == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'path' field", nil, nil))
		return
	}
	abs := resolvePath(dir)
	if err := os.MkdirAll(abs, 0755); err != nil {
		sendJSON(w, 500, fail("FILE_ERROR", "mkdir failed: "+err.Error(), nil, nil))
		return
	}
	sendJSON(w, 200, ok(map[string]interface{}{"path": abs, "created": true}, nil))
}

func handleFilesDelete(w http.ResponseWriter, r *http.Request) {
	filePath := queryStr(r, "path")
	if filePath == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'path' query param", nil, nil))
		return
	}
	abs := resolvePath(filePath)
	if err := os.RemoveAll(abs); err != nil {
		sendJSON(w, 500, fail("FILE_ERROR", "Delete failed: "+err.Error(), nil, nil))
		return
	}
	sendJSON(w, 200, ok(map[string]interface{}{"path": abs, "deleted": true}, nil))
}

func handleFilesMove(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	obj, err := parseJSONBody(body)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	from := stringField(obj, "from")
	to := stringField(obj, "to")
	if from == "" || to == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'from' or 'to' field", nil, nil))
		return
	}
	absFrom := resolvePath(from)
	absTo := resolvePath(to)
	if err := os.MkdirAll(filepath.Dir(absTo), 0755); err != nil {
		sendJSON(w, 500, fail("FILE_ERROR", "Move failed: "+err.Error(), nil, nil))
		return
	}
	if err := os.Rename(absFrom, absTo); err != nil {
		sendJSON(w, 500, fail("FILE_ERROR", "Move failed: "+err.Error(), nil, nil))
		return
	}
	sendJSON(w, 200, ok(map[string]interface{}{"from": absFrom, "to": absTo, "moved": true}, nil))
}

func handleFilesStat(w http.ResponseWriter, r *http.Request) {
	filePath := queryStr(r, "path")
	if filePath == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'path' query param", nil, nil))
		return
	}
	abs := resolvePath(filePath)
	info, err := os.Stat(abs)
	if err != nil {
		sendJSON(w, 500, fail("FILE_ERROR", "stat failed: "+err.Error(), nil, nil))
		return
	}
	sendJSON(w, 200, ok(map[string]interface{}{
		"path":      abs,
		"size":      info.Size(),
		"isFile":    !info.IsDir(),
		"isDir":     info.IsDir(),
		"mode":      fileModeString(info.Mode()),
		"mtime":     info.ModTime().UTC().Format("2006-01-02T15:04:05Z07:00"),
		"birthtime": info.ModTime().UTC().Format("2006-01-02T15:04:05Z07:00"),
	}, nil))
}

func handleFilesSearch(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	obj, err := parseJSONBody(body)
	if err != nil {
		sendJSON(w, 400, fail("BAD_REQUEST", err.Error(), nil, nil))
		return
	}
	pattern := stringField(obj, "pattern")
	if pattern == "" {
		sendJSON(w, 400, fail("MISSING_PARAM", "Missing 'pattern' field", nil, nil))
		return
	}
	searchPath := stringField(obj, "path")
	if searchPath == "" {
		searchPath = "."
	}
	recursive := true
	if v, ok := obj["recursive"].(bool); ok {
		recursive = v
	}
	maxResults := boundedInt(obj["maxResults"], 100, 1, 10000)

	re, err := regexp.Compile(pattern)
	if err != nil {
		sendJSON(w, 400, fail("INVALID_PARAM", "Invalid regex pattern: "+err.Error(), nil, nil))
		return
	}
	results := grepSearch(resolvePath(searchPath), re, recursive, maxResults)
	sendJSON(w, 200, ok(results, map[string]interface{}{"count": len(results)}))
}

func grepSearch(dir string, re *regexp.Regexp, recursive bool, maxResults int) []map[string]interface{} {
	results := []map[string]interface{}{}
	var walk func(d string)
	walk = func(d string) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			if len(results) >= maxResults {
				return
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" {
				continue
			}
			full := filepath.Join(d, name)
			if e.IsDir() && recursive {
				walk(full)
			} else if !e.IsDir() {
				data, err := os.ReadFile(full)
				if err != nil {
					continue
				}
				lines := strings.Split(string(data), "\n")
				for i, line := range lines {
					if re.MatchString(line) {
						results = append(results, map[string]interface{}{
							"file":    full,
							"line":    i + 1,
							"content": strings.TrimSpace(line),
						})
						if len(results) >= maxResults {
							return
						}
					}
				}
			}
		}
	}
	walk(dir)
	return results
}

func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return filepath.Clean(abs)
}

func fileModeString(mode os.FileMode) string {
	// Represent as a numeric octal string, e.g. 0644 -> "644".
	perm := mode.Perm()
	return itoa(int(perm))
}
