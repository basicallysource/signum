package driver

import (
	"archive/zip"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"
)

// A Bambu printer keeps the project it is printing on its own storage, and
// the same access code that opens MQTT opens FTPS. The 3MF inside is the
// slicer's whole testimony: every process setting, which of them stray from
// the system presets, and which objects are on the plate -- including
// per-object overrides, which is what makes one part's infill different
// from its plate-mates.
//
// This file turns that into one JSON document:
//
//	{"plate": 5,
//	 "settings": {"wall_loops": "2", ...},
//	 "changed": ["wall_loops", ...],
//	 "objects": [{"name": "arm-ni9m0h.stl",
//	              "overrides": {"sparse_infill_density": "20%"}}]}

const (
	bambuFTPSPort     = 990
	bambuFetchTimeout = 30 * time.Second
	// bambuProjectCap bounds one download; a plate's 3MF is usually a few
	// MB and never legitimately this.
	bambuProjectCap = 256 << 20
)

var bambuPlateSuffix = regexp.MustCompile(`_plate_([0-9]+)$`)

// fetchSliceDoc pulls the project for a job named like the printer names
// them ("thing_plate_5") and distills it. A modern send is stored as the
// subtask name verbatim -- plate suffix included -- as a per-plate 3MF; the
// suffix-stripped whole project is the older layout.
func fetchSliceDoc(host, accessCode, subtask string) (json.RawMessage, error) {
	base := subtask
	plate := 1
	if match := bambuPlateSuffix.FindStringSubmatch(subtask); match != nil {
		base = strings.TrimSuffix(subtask, match[0])
		plate, _ = strconv.Atoi(match[1])
	}

	archive, err := fetchProject(host, accessCode, []string{
		"/cache/" + subtask + ".3mf",
		"/cache/" + base + ".3mf",
		"/" + subtask + ".3mf",
		"/model/" + base + ".3mf",
	})
	if err != nil {
		return nil, err
	}
	return distill3MF(archive, plate)
}

// fetchProject retrieves the first of paths that exists on the printer over
// implicit-TLS FTPS. The certificate is self-signed and the access code is
// the trust; the session cache matters because some firmwares insist the
// data connection resumes the control connection's TLS session.
func fetchProject(host, accessCode string, paths []string) ([]byte, error) {
	config := &tls.Config{
		InsecureSkipVerify: true,
		ClientSessionCache: tls.NewLRUClientSessionCache(8),
	}
	client, err := ftp.Dial(fmt.Sprintf("%s:%d", host, bambuFTPSPort),
		ftp.DialWithTLS(config), ftp.DialWithTimeout(bambuFetchTimeout))
	if err != nil {
		return nil, fmt.Errorf("driver: reach %s ftps: %w", host, err)
	}
	defer client.Quit()

	if err := client.Login("bblp", accessCode); err != nil {
		return nil, fmt.Errorf("driver: %s refused the access code: %w", host, err)
	}

	var lastErr error
	for _, path := range paths {
		response, err := client.Retr(path)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(response, bambuProjectCap))
		response.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return data, nil
	}
	return nil, fmt.Errorf("driver: %s has no project at %s: %w", host, strings.Join(paths, " "), lastErr)
}

// distill3MF reads the three config entries that matter out of the project
// archive and builds the slice document for one plate.
func distill3MF(archive []byte, plate int) (json.RawMessage, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("driver: project is not a 3mf: %w", err)
	}

	settings, changed := projectSettings(reader)
	overrides := modelOverrides(reader)
	objects := plateObjects(reader, plate)

	type docObject struct {
		Name      string            `json:"name"`
		Overrides map[string]string `json:"overrides,omitempty"`
	}
	doc := struct {
		Plate    int               `json:"plate"`
		Settings map[string]string `json:"settings,omitempty"`
		Changed  []string          `json:"changed,omitempty"`
		Objects  []docObject       `json:"objects,omitempty"`
	}{Plate: plate, Settings: settings, Changed: changed}

	for _, name := range objects {
		doc.Objects = append(doc.Objects, docObject{Name: name, Overrides: overrides[name]})
	}
	if len(doc.Settings) == 0 && len(doc.Objects) == 0 {
		return nil, fmt.Errorf("driver: the 3mf carried no settings or objects")
	}
	return json.Marshal(doc)
}

func readEntry(reader *zip.Reader, name string) []byte {
	for _, entry := range reader.File {
		if entry.Name != name {
			continue
		}
		file, err := entry.Open()
		if err != nil {
			return nil
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 32<<20))
		if err != nil {
			return nil
		}
		return data
	}
	return nil
}

// projectSettings flattens Metadata/project_settings.config: every process
// setting as a string, plus the keys the slicer itself says stray from the
// system presets ("different_settings_to_system", semicolon-joined lists,
// one per preset).
func projectSettings(reader *zip.Reader) (map[string]string, []string) {
	raw := readEntry(reader, "Metadata/project_settings.config")
	if raw == nil {
		return nil, nil
	}
	var parsed map[string]any
	if json.Unmarshal(raw, &parsed) != nil {
		return nil, nil
	}

	changedSet := map[string]bool{}
	if lists, ok := parsed["different_settings_to_system"].([]any); ok {
		for _, list := range lists {
			if joined, ok := list.(string); ok {
				for _, key := range strings.Split(joined, ";") {
					if key = strings.TrimSpace(key); key != "" {
						changedSet[key] = true
					}
				}
			}
		}
	}
	delete(parsed, "different_settings_to_system")

	settings := make(map[string]string, len(parsed))
	for key, value := range parsed {
		if flat := flatten(value); flat != "" {
			settings[key] = flat
		}
	}
	changed := make([]string, 0, len(changedSet))
	for key := range changedSet {
		changed = append(changed, key)
	}
	sort.Strings(changed)
	return settings, changed
}

func flatten(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case []any:
		var parts []string
		for _, item := range v {
			parts = append(parts, flatten(item))
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

type xmlKV struct {
	Key   string `xml:"key,attr"`
	Value string `xml:"value,attr"`
}

// plateObjects reads Metadata/slice_info.config: which objects were sliced
// onto this plate, skipping the skipped.
func plateObjects(reader *zip.Reader, plate int) []string {
	raw := readEntry(reader, "Metadata/slice_info.config")
	if raw == nil {
		return nil
	}
	var parsed struct {
		Plates []struct {
			Metadata []xmlKV `xml:"metadata"`
			Objects  []struct {
				Name    string `xml:"name,attr"`
				Skipped string `xml:"skipped,attr"`
			} `xml:"object"`
		} `xml:"plate"`
	}
	if xml.Unmarshal(raw, &parsed) != nil {
		return nil
	}

	for _, candidate := range parsed.Plates {
		index := 0
		for _, kv := range candidate.Metadata {
			if kv.Key == "index" {
				index, _ = strconv.Atoi(kv.Value)
			}
		}
		if index != plate && len(parsed.Plates) > 1 {
			continue
		}
		var names []string
		for _, object := range candidate.Objects {
			if object.Skipped == "true" {
				continue
			}
			names = append(names, object.Name)
		}
		return names
	}
	return nil
}

// modelOverrides reads Metadata/model_settings.config: the per-object
// settings a person changed on specific parts, keyed by object name.
func modelOverrides(reader *zip.Reader) map[string]map[string]string {
	raw := readEntry(reader, "Metadata/model_settings.config")
	if raw == nil {
		return nil
	}
	var parsed struct {
		Objects []struct {
			Metadata []xmlKV `xml:"metadata"`
		} `xml:"object"`
	}
	if xml.Unmarshal(raw, &parsed) != nil {
		return nil
	}

	overrides := map[string]map[string]string{}
	for _, object := range parsed.Objects {
		name := ""
		values := map[string]string{}
		for _, kv := range object.Metadata {
			switch kv.Key {
			case "name":
				name = kv.Value
			case "extruder":
				// Which extruder is plumbing, not a person's choice.
			default:
				values[kv.Key] = kv.Value
			}
		}
		if name != "" && len(values) > 0 {
			overrides[name] = values
		}
	}
	return overrides
}
