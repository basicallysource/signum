package driver

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"testing"
)

func project3MF(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		entry.Write([]byte(content))
	}
	writer.Close()
	return buffer.Bytes()
}

func TestDistill3MF(t *testing.T) {
	archive := project3MF(t, map[string]string{
		"Metadata/project_settings.config": `{
			"wall_loops": "2",
			"sparse_infill_density": "15%",
			"sparse_infill_pattern": "grid",
			"nozzle_temperature": ["220"],
			"enable_support": false,
			"different_settings_to_system": ["wall_loops;sparse_infill_density", "", "nozzle_temperature"]
		}`,
		"Metadata/slice_info.config": `<?xml version="1.0" encoding="UTF-8"?>
<config>
  <plate>
    <metadata key="index" value="4"/>
    <object identify_id="101" name="ring-rdjuxb.stl" skipped="false"/>
  </plate>
  <plate>
    <metadata key="index" value="5"/>
    <object identify_id="201" name="arm-ni9m0h.stl" skipped="false"/>
    <object identify_id="202" name="scrap.stl" skipped="true"/>
    <object identify_id="203" name="clamp-1e62t2.stl" skipped="false"/>
  </plate>
</config>`,
		"Metadata/model_settings.config": `<?xml version="1.0" encoding="UTF-8"?>
<config>
  <object id="7">
    <metadata key="name" value="arm-ni9m0h.stl"/>
    <metadata key="extruder" value="1"/>
    <metadata key="sparse_infill_density" value="40%"/>
  </object>
</config>`,
	})

	raw, err := distill3MF(archive, 5)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Plate    int               `json:"plate"`
		Settings map[string]string `json:"settings"`
		Changed  []string          `json:"changed"`
		Objects  []struct {
			Name      string            `json:"name"`
			Overrides map[string]string `json:"overrides"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	if doc.Settings["wall_loops"] != "2" || doc.Settings["nozzle_temperature"] != "220" ||
		doc.Settings["enable_support"] != "false" {
		t.Fatalf("settings not flattened: %+v", doc.Settings)
	}
	if len(doc.Changed) != 3 || doc.Changed[0] != "nozzle_temperature" {
		t.Fatalf("changed keys wrong: %v", doc.Changed)
	}

	// Plate 5's objects, in order, skipped one dropped, override attached to
	// the right object only.
	if len(doc.Objects) != 2 || doc.Objects[0].Name != "arm-ni9m0h.stl" ||
		doc.Objects[1].Name != "clamp-1e62t2.stl" {
		t.Fatalf("objects wrong: %+v", doc.Objects)
	}
	if doc.Objects[0].Overrides["sparse_infill_density"] != "40%" {
		t.Fatalf("override missing: %+v", doc.Objects[0])
	}
	if len(doc.Objects[1].Overrides) != 0 {
		t.Fatalf("override leaked: %+v", doc.Objects[1])
	}
	if _, ok := doc.Objects[0].Overrides["extruder"]; ok {
		t.Fatalf("extruder is plumbing, not an override: %+v", doc.Objects[0])
	}
}

func TestBambuMachineAttachSlice(t *testing.T) {
	m := newBambuMachine("p1s")
	m.Apply([]byte(`{"print":{"gcode_state":"RUNNING","subtask_name":"x","task_id":"9"}}`), at(0))
	m.attachSlice("9", []byte(`{"plate":1}`))
	if jobs := m.Jobs(); string(jobs[0].Slice) != `{"plate":1}` {
		t.Fatalf("slice not attached: %+v", jobs[0])
	}
	// Unknown ids are ignored quietly.
	m.attachSlice("nope", []byte(`{}`))
}
