package driver

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/basicallysource/signum/internal/printwatch"
)

// A Bambu printer narrates itself as a stream of partial JSON reports over
// MQTT: any message may carry any subset of the printer's state, and a
// full snapshot arrives on request. bambuMachine folds that stream into
// jobs. It is pure -- payload in, jobs out -- so the whole protocol's logic
// is testable without a printer or a broker.
type bambuMachine struct {
	printer string

	// known is the merged view of every field a report has mentioned.
	known bambuKnown

	jobs    map[string]printwatch.Job
	order   []string
	current string
	// opened distinguishes "no job yet" from "job numbered n": the fallback
	// external id for LAN prints, which have no task id, needs to be unique
	// across same-named reprints.
	opened int
}

type bambuKnown struct {
	state       string
	subtask     string
	taskID      string
	percent     int
	layer       int
	totalLayers int
	nozzle      float64
	bed         float64
	speed       int
	filament    string
}

// The states a Bambu reports in gcode_state.
const (
	bambuIdle    = "IDLE"
	bambuPrepare = "PREPARE"
	bambuRunning = "RUNNING"
	bambuPause   = "PAUSE"
	bambuFinish  = "FINISH"
	bambuFailed  = "FAILED"
)

// keepFinished is how long a finished job stays in the snapshot. The server
// heard about it long before this; the window only bridges flaky reporting.
const keepFinished = 24 * time.Hour

func newBambuMachine(printer string) *bambuMachine {
	return &bambuMachine{printer: printer, jobs: make(map[string]printwatch.Job)}
}

// bambuReport is the slice of a report this machine reads. Pointers detect
// presence: a partial report only carries what changed.
type bambuReport struct {
	Print *bambuPrint `json:"print"`
}

type bambuPrint struct {
	GcodeState    *string    `json:"gcode_state"`
	SubtaskName   *string    `json:"subtask_name"`
	TaskID        *string    `json:"task_id"`
	McPercent     *float64   `json:"mc_percent"`
	LayerNum      *float64   `json:"layer_num"`
	TotalLayerNum *float64   `json:"total_layer_num"`
	NozzleTemper  *float64   `json:"nozzle_temper"`
	BedTemper     *float64   `json:"bed_temper"`
	SpdLvl        *float64   `json:"spd_lvl"`
	AMS           *bambuAMS  `json:"ams"`
	VtTray        *bambuTray `json:"vt_tray"`
}

type bambuAMS struct {
	TrayNow *string `json:"tray_now"`
	Units   []struct {
		ID   string      `json:"id"`
		Tray []bambuTray `json:"tray"`
	} `json:"ams"`
}

type bambuTray struct {
	ID        string `json:"id"`
	TrayType  string `json:"tray_type"`
	TrayColor string `json:"tray_color"`
}

// Apply folds one report into the state.
func (m *bambuMachine) Apply(payload []byte, now time.Time) {
	var report bambuReport
	if err := json.Unmarshal(payload, &report); err != nil || report.Print == nil {
		return
	}
	p := report.Print

	if p.SubtaskName != nil {
		m.known.subtask = *p.SubtaskName
	}
	if p.TaskID != nil {
		m.known.taskID = *p.TaskID
	}
	if p.McPercent != nil {
		m.known.percent = int(*p.McPercent)
	}
	if p.LayerNum != nil {
		m.known.layer = int(*p.LayerNum)
	}
	if p.TotalLayerNum != nil {
		m.known.totalLayers = int(*p.TotalLayerNum)
	}
	if p.NozzleTemper != nil {
		m.known.nozzle = *p.NozzleTemper
	}
	if p.BedTemper != nil {
		m.known.bed = *p.BedTemper
	}
	if p.SpdLvl != nil {
		m.known.speed = int(*p.SpdLvl)
	}
	if filament := bambuFilament(p); filament != "" {
		m.known.filament = filament
	}
	if p.GcodeState != nil {
		m.known.state = *p.GcodeState
		m.transition(*p.GcodeState, now)
	}
	// Opening a job wipes the previous print's progress; numbers that rode
	// in on this same payload are the new print's own and survive it.
	if p.McPercent != nil {
		m.known.percent = int(*p.McPercent)
	}
	if p.LayerNum != nil {
		m.known.layer = int(*p.LayerNum)
	}
	if p.TotalLayerNum != nil {
		m.known.totalLayers = int(*p.TotalLayerNum)
	}

	m.refresh(now)
}

func (m *bambuMachine) transition(next string, now time.Time) {
	printing := next == bambuPrepare || next == bambuRunning || next == bambuPause

	switch {
	case printing && m.current == "":
		m.open(now)
	case printing && m.renamed():
		// A new print started over the record of the old one: whatever the
		// old one was doing, it is over.
		if m.active() {
			m.close(printwatch.StatusCanceled, now)
		} else {
			m.current = ""
		}
		m.open(now)
	case next == bambuFinish && m.active():
		m.close(printwatch.StatusSucceeded, now)
	case next == bambuFailed && m.active():
		m.close(printwatch.StatusFailed, now)
	case next == bambuIdle && m.active():
		// Straight from printing to idle, with no FINISH or FAILED between,
		// is what a cancel looks like from outside.
		m.close(printwatch.StatusCanceled, now)
	}
}

// renamed reports that the printer is now printing something other than what
// the current job records. A job observed before its name arrived is not a
// rename; refresh names it in place.
func (m *bambuMachine) renamed() bool {
	if m.current == "" || m.known.subtask == "" {
		return false
	}
	name := m.jobs[m.current].Filename
	return name != "" && name != m.known.subtask
}

func (m *bambuMachine) active() bool {
	if m.current == "" {
		return false
	}
	return m.jobs[m.current].Status == printwatch.StatusPrinting
}

func (m *bambuMachine) open(now time.Time) {
	// The printer keeps narrating the LAST print's progress until the new
	// one actually lays gcode: percent 100, full layer count. A fresh job
	// starts knowing nothing and believes only numbers that arrive after
	// it opened.
	m.known.percent, m.known.layer, m.known.totalLayers = 0, 0, 0
	m.opened++
	id := m.known.taskID
	// LAN-mode prints report no usable task id; number the observation.
	if id == "" || id == "0" {
		id = fmt.Sprintf("seen-%d-%d", now.Unix(), m.opened)
	}
	if _, taken := m.jobs[id]; taken {
		id = fmt.Sprintf("%s-again-%d", id, m.opened)
	}
	m.jobs[id] = printwatch.Job{
		ExternalID: id,
		Filename:   m.known.subtask,
		Status:     printwatch.StatusPrinting,
		StartedAt:  now,
	}
	m.order = append(m.order, id)
	m.current = id
}

func (m *bambuMachine) close(status string, now time.Time) {
	job := m.jobs[m.current]
	job.Status = status
	job.EndedAt = now
	m.jobs[m.current] = job
	m.current = ""
}

// refresh keeps the active job's name and parameters current, and prunes
// long-finished jobs from the snapshot.
func (m *bambuMachine) refresh(now time.Time) {
	if m.current != "" {
		job := m.jobs[m.current]
		if job.Filename == "" {
			job.Filename = m.known.subtask
		}
		job.Params = m.params()
		m.jobs[m.current] = job
	}

	kept := m.order[:0]
	for _, id := range m.order {
		job := m.jobs[id]
		if job.Done() && now.Sub(job.EndedAt) > keepFinished {
			delete(m.jobs, id)
			continue
		}
		kept = append(kept, id)
	}
	m.order = kept
}

func (m *bambuMachine) params() map[string]string {
	params := map[string]string{}
	if m.known.filament != "" {
		params["filament"] = m.known.filament
	}
	if m.known.nozzle > 0 {
		params["nozzle"] = strconv.FormatFloat(m.known.nozzle, 'f', 0, 64) + "C"
	}
	if m.known.bed > 0 {
		params["bed"] = strconv.FormatFloat(m.known.bed, 'f', 0, 64) + "C"
	}
	if m.known.totalLayers > 0 {
		params["layers"] = fmt.Sprintf("%d/%d", m.known.layer, m.known.totalLayers)
	}
	if m.known.percent > 0 {
		params["percent"] = strconv.Itoa(m.known.percent)
	}
	if name := bambuSpeed(m.known.speed); name != "" {
		params["speed"] = name
	}
	return params
}

// Jobs is the current snapshot, oldest first.
func (m *bambuMachine) Jobs() []printwatch.Job {
	jobs := make([]printwatch.Job, 0, len(m.order))
	for _, id := range m.order {
		jobs = append(jobs, m.jobs[id])
	}
	return jobs
}

// bambuFilament names the filament actually feeding the print: the active
// AMS tray, or the external spool. Tray 254 is the external spool; 255 is
// none loaded.
func bambuFilament(p *bambuPrint) string {
	if p.AMS != nil && p.AMS.TrayNow != nil {
		now, err := strconv.Atoi(*p.AMS.TrayNow)
		if err == nil && now < 254 {
			for _, unit := range p.AMS.Units {
				unitIndex, _ := strconv.Atoi(unit.ID)
				for _, tray := range unit.Tray {
					trayIndex, _ := strconv.Atoi(tray.ID)
					if unitIndex*4+trayIndex == now {
						return describeTray(tray)
					}
				}
			}
		}
		if err == nil && now == 254 && p.VtTray != nil {
			return describeTray(*p.VtTray)
		}
	}
	if p.VtTray != nil && p.AMS == nil {
		return describeTray(*p.VtTray)
	}
	return ""
}

func describeTray(tray bambuTray) string {
	if tray.TrayType == "" {
		return ""
	}
	if color := strings.TrimSuffix(tray.TrayColor, "FF"); len(color) == 6 {
		return tray.TrayType + " #" + color
	}
	return tray.TrayType
}

// bambuSpeed names the printer's speed level.
func bambuSpeed(level int) string {
	switch level {
	case 1:
		return "silent"
	case 2:
		return "standard"
	case 3:
		return "sport"
	case 4:
		return "ludicrous"
	}
	return ""
}
