package ops

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type scheduleState struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Enabled           bool      `json:"enabled"`
	Type              string    `json:"type"`
	At                time.Time `json:"at,omitempty"`
	EverySeconds      int64     `json:"everySeconds,omitempty"`
	TimeOfDay         string    `json:"timeOfDay,omitempty"`
	Weekday           int       `json:"weekday,omitempty"`
	Cron              string    `json:"cron,omitempty"`
	Target            Request   `json:"target"`
	RetryCount        int       `json:"retryCount"`
	RetryDelaySeconds int64     `json:"retryDelaySeconds"`
	CreatedAt         time.Time `json:"createdAt"`
	NextRunAt         time.Time `json:"nextRunAt,omitempty"`
	LastRunAt         time.Time `json:"lastRunAt,omitempty"`
	LastResult        Result    `json:"lastResult,omitempty"`
	RunCount          int64     `json:"runCount"`
}

type scheduleView scheduleState

type scheduleEvent struct {
	Kind string
	At   time.Time
}

func (m *Manager) scheduleOperation(_ context.Context, req Request) Result {
	action, err := actionOf(req, "list")
	if err != nil {
		return invalid(err.Error())
	}
	if action == "remove" {
		action = "delete"
	}
	if action == "run" {
		action = "run_now"
	}
	if err := m.ensureRuntimeDirs(); err != nil {
		return fileFailure("定时任务状态目录不可用", err, "schedule_store")
	}
	m.ensureSchedulesLoaded()
	switch action {
	case "list":
		m.schedulesMu.RLock()
		views := make([]scheduleView, 0, len(m.schedules))
		for _, schedule := range m.schedules {
			views = append(views, scheduleSnapshot(schedule))
		}
		m.schedulesMu.RUnlock()
		return ok("定时任务列表读取成功", map[string]any{"schedules": views, "eventListeners": m.scheduleListenerStatus()}, "scheduler_loop")
	case "get", "history":
		id, parseErr := argString(req.Args, "id", "scheduleId")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		m.schedulesMu.RLock()
		schedule, exists := m.schedules[id]
		if !exists {
			m.schedulesMu.RUnlock()
			return fail("NOT_FOUND", "定时任务不存在", os.ErrNotExist, "schedule_store")
		}
		view := scheduleSnapshot(schedule)
		m.schedulesMu.RUnlock()
		if action == "history" {
			return ok("定时任务最近执行结果读取成功", map[string]any{"id": id, "lastRunAt": view.LastRunAt, "runCount": view.RunCount, "lastResult": view.LastResult}, "schedule_store")
		}
		return ok("定时任务读取成功", view, "schedule_store")
	case "create", "update":
		schedule, parseErr := m.parseSchedule(req.Args)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		if action == "update" {
			id, idErr := argString(req.Args, "id", "scheduleId")
			if idErr != nil {
				return invalid(idErr.Error())
			}
			schedule.ID = id
		}
		m.schedulesMu.Lock()
		_, exists := m.schedules[schedule.ID]
		if action == "create" && exists {
			m.schedulesMu.Unlock()
			return fail("ALREADY_EXISTS", "定时任务 ID 已存在", os.ErrExist, "schedule_store")
		}
		m.schedules[schedule.ID] = schedule
		m.schedulesMu.Unlock()
		if err := m.persistSchedule(schedule); err != nil {
			return fileFailure("定时任务持久化失败", err, "schedule_store")
		}
		m.signalScheduler()
		return ok("定时任务已保存", scheduleSnapshot(schedule), "scheduler_loop")
	case "delete":
		id, parseErr := argString(req.Args, "id", "scheduleId")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		m.schedulesMu.Lock()
		_, exists := m.schedules[id]
		if exists {
			delete(m.schedules, id)
		}
		m.schedulesMu.Unlock()
		if !exists {
			return fail("NOT_FOUND", "定时任务不存在", os.ErrNotExist, "schedule_store")
		}
		if err := os.Remove(m.schedulePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fileFailure("定时任务文件删除失败", err, "schedule_store")
		}
		m.signalScheduler()
		return ok("定时任务已删除", map[string]string{"id": id}, "schedule_store")
	case "enable", "disable":
		id, parseErr := argString(req.Args, "id", "scheduleId")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		m.schedulesMu.Lock()
		schedule, exists := m.schedules[id]
		if !exists {
			m.schedulesMu.Unlock()
			return fail("NOT_FOUND", "定时任务不存在", os.ErrNotExist, "schedule_store")
		}
		schedule.Enabled = action == "enable"
		if schedule.Enabled {
			schedule.NextRunAt, _ = scheduleNext(schedule, time.Now())
		} else {
			schedule.NextRunAt = time.Time{}
		}
		view := scheduleSnapshot(schedule)
		m.schedulesMu.Unlock()
		if err := m.persistSchedule(schedule); err != nil {
			return fileFailure("定时任务状态持久化失败", err, "schedule_store")
		}
		m.signalScheduler()
		return ok("定时任务状态已修改", view, "scheduler_loop")
	case "run_now":
		id, parseErr := argString(req.Args, "id", "scheduleId")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		m.schedulesMu.RLock()
		_, exists := m.schedules[id]
		m.schedulesMu.RUnlock()
		if !exists {
			return fail("NOT_FOUND", "定时任务不存在", os.ErrNotExist, "schedule_store")
		}
		go m.executeSchedule(id)
		return ok("定时任务已立即提交", map[string]string{"id": id}, "schedule_manual_trigger")
	default:
		return invalidAction(req.Tool, action, "create", "delete", "disable", "enable", "get", "history", "list", "remove", "run", "run_now", "update")
	}
}

func (m *Manager) parseSchedule(args map[string]any) (*scheduleState, error) {
	kind, err := argString(args, "type")
	if err != nil {
		return nil, err
	}
	kind = normalizeTool(kind)
	switch kind {
	case "once", "interval", "daily", "weekly", "cron", "boot", "network", "charging":
	default:
		return nil, errors.New("type 必须是 once、interval、daily、weekly、cron、boot、network 或 charging")
	}
	id, _ := argOptionalString(args, randomTaskID(), "id", "scheduleId")
	if _, err := safeScheduleID(id); err != nil {
		return nil, err
	}
	name, _ := argOptionalString(args, id, "name")
	targetTool, err := argString(args, "tool", "targetTool")
	targetArgs := map[string]any{}
	if err != nil {
		command, exists := args["command"].(string)
		if !exists || command == "" {
			return nil, errors.New("定时任务需要 targetTool/tool 或 command")
		}
		targetTool = "zcr521_shell"
		targetArgs = map[string]any{"action": "exec", "command": command}
	} else if raw, exists := args["targetArgs"]; exists {
		typed, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("targetArgs 必须是对象")
		}
		targetArgs = copyArgs(typed)
	}
	targetTool = normalizeTool(targetTool)
	if targetTool == "zcr521_schedule" {
		return nil, errors.New("定时任务不能递归调用 zcr521_schedule")
	}
	enabled, err := argBool(args, "enabled", true)
	if err != nil {
		return nil, err
	}
	retries, err := argInt64(args, 0, "retryCount", "retries")
	if err != nil || retries < 0 || retries > 10 {
		return nil, errors.New("retryCount 必须在 0 到 10 之间")
	}
	retryDelay, err := argInt64(args, 30, "retryDelaySeconds")
	if err != nil || retryDelay < 1 || retryDelay > 86400 {
		return nil, errors.New("retryDelaySeconds 必须在 1 到 86400 之间")
	}
	schedule := &scheduleState{
		ID: id, Name: name, Enabled: enabled, Type: kind,
		Target:     Request{Tool: targetTool, Args: targetArgs},
		RetryCount: int(retries), RetryDelaySeconds: retryDelay,
		CreatedAt:  time.Now().UTC(),
		LastResult: Result{Success: false, Code: "NEVER_RUN", Message: "尚未执行", ExitCode: -1},
	}
	switch kind {
	case "once":
		raw, parseErr := argString(args, "at")
		if parseErr != nil {
			return nil, parseErr
		}
		schedule.At, parseErr = time.Parse(time.RFC3339, raw)
		if parseErr != nil || !schedule.At.After(time.Now()) {
			return nil, errors.New("once.at 必须是未来的 RFC3339 时间")
		}
	case "interval":
		schedule.EverySeconds, err = argInt64(args, 0, "everySeconds", "intervalSeconds")
		if err != nil || schedule.EverySeconds < 1 {
			return nil, errors.New("interval.everySeconds 必须大于 0")
		}
	case "daily", "weekly":
		schedule.TimeOfDay, err = argString(args, "time")
		if err != nil || !validTimeOfDay(schedule.TimeOfDay) {
			return nil, errors.New("time 必须是 HH:MM 24 小时格式")
		}
		if kind == "weekly" {
			weekday, parseErr := argInt64(args, -1, "weekday")
			if parseErr != nil || weekday < 0 || weekday > 6 {
				return nil, errors.New("weekday 必须为 0..6，0 表示周日")
			}
			schedule.Weekday = int(weekday)
		}
	case "cron":
		schedule.Cron, err = argString(args, "cron", "expression")
		if err != nil {
			return nil, err
		}
		if _, parseErr := nextCronTime(schedule.Cron, time.Now()); parseErr != nil {
			return nil, parseErr
		}
	}
	if enabled {
		schedule.NextRunAt, err = scheduleNext(schedule, time.Now())
		if err != nil {
			return nil, err
		}
	}
	return schedule, nil
}

func (m *Manager) ensureSchedulesLoaded() {
	m.scheduleOnce.Do(func() {
		root := filepath.Join(m.cfg.StateDir, "schedules")
		entries, err := os.ReadDir(root)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				raw, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
				if readErr != nil {
					continue
				}
				var schedule scheduleState
				if json.Unmarshal(raw, &schedule) != nil {
					continue
				}
				if _, idErr := safeScheduleID(schedule.ID); idErr != nil {
					continue
				}
				if schedule.Enabled {
					schedule.NextRunAt, _ = scheduleNext(&schedule, time.Now())
				}
				m.schedules[schedule.ID] = &schedule
			}
		}
		go m.schedulerLoop()
	})
}

func (m *Manager) schedulerLoop() {
	var timer *time.Timer
	for {
		m.reconcileScheduleEventListeners()
		m.schedulesMu.RLock()
		next, hasNext := nearestScheduleTime(m.schedules)
		m.schedulesMu.RUnlock()
		var timerChannel <-chan time.Time
		if hasNext {
			delay := time.Until(next)
			if delay < 0 {
				delay = 0
			}
			if timer == nil {
				timer = time.NewTimer(delay)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(delay)
			}
			timerChannel = timer.C
		} else if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		select {
		case <-m.scheduleChanges:
		case event := <-m.scheduleEvents:
			m.triggerScheduleEvent(event)
		case <-timerChannel:
			m.triggerDueSchedules(time.Now())
		}
	}
}

func nearestScheduleTime(schedules map[string]*scheduleState) (time.Time, bool) {
	var nearest time.Time
	for _, schedule := range schedules {
		if !schedule.Enabled || schedule.NextRunAt.IsZero() {
			continue
		}
		if nearest.IsZero() || schedule.NextRunAt.Before(nearest) {
			nearest = schedule.NextRunAt
		}
	}
	return nearest, !nearest.IsZero()
}

func (m *Manager) triggerDueSchedules(now time.Time) {
	due := make([]string, 0)
	toPersist := make([]*scheduleState, 0)
	m.schedulesMu.Lock()
	for id, schedule := range m.schedules {
		if !schedule.Enabled || schedule.NextRunAt.IsZero() || schedule.NextRunAt.After(now) {
			continue
		}
		due = append(due, id)
		if schedule.Type == "once" || schedule.Type == "boot" {
			schedule.Enabled = false
			schedule.NextRunAt = time.Time{}
		} else {
			schedule.NextRunAt, _ = scheduleNext(schedule, now)
		}
		toPersist = append(toPersist, schedule)
	}
	m.schedulesMu.Unlock()
	for _, schedule := range toPersist {
		_ = m.persistSchedule(schedule)
	}
	for _, id := range due {
		go m.executeSchedule(id)
	}
}

func (m *Manager) triggerScheduleEvent(event scheduleEvent) {
	now := event.At
	if now.IsZero() {
		now = time.Now()
	}
	ids := make([]string, 0)
	m.schedulesMu.RLock()
	for id, schedule := range m.schedules {
		if schedule.Enabled && schedule.Type == event.Kind && (schedule.LastRunAt.IsZero() || now.Sub(schedule.LastRunAt) >= 30*time.Second) {
			ids = append(ids, id)
		}
	}
	m.schedulesMu.RUnlock()
	for _, id := range ids {
		go m.executeSchedule(id)
	}
}

func (m *Manager) executeSchedule(id string) {
	m.schedulesMu.RLock()
	schedule, exists := m.schedules[id]
	if !exists {
		m.schedulesMu.RUnlock()
		return
	}
	target := Request{Tool: schedule.Target.Tool, Args: copyArgs(schedule.Target.Args)}
	retryCount := schedule.RetryCount
	retryDelay := schedule.RetryDelaySeconds
	m.schedulesMu.RUnlock()
	var result Result
	for attempt := 0; attempt <= retryCount; attempt++ {
		result = m.Execute(context.Background(), target)
		if result.Success {
			break
		}
		if attempt < retryCount {
			time.Sleep(time.Duration(retryDelay) * time.Second)
		}
	}
	m.schedulesMu.Lock()
	current, stillExists := m.schedules[id]
	if stillExists {
		current.LastRunAt = time.Now().UTC()
		current.LastResult = result
		current.RunCount++
	}
	m.schedulesMu.Unlock()
	if stillExists {
		_ = m.persistSchedule(current)
	}
}

func (m *Manager) signalScheduler() {
	select {
	case m.scheduleChanges <- struct{}{}:
	default:
	}
}

func (m *Manager) reconcileScheduleEventListeners() {
	needed := map[string]bool{"network": false, "charging": false}
	m.schedulesMu.RLock()
	for _, schedule := range m.schedules {
		if schedule.Enabled {
			if _, exists := needed[schedule.Type]; exists {
				needed[schedule.Type] = true
			}
		}
	}
	m.schedulesMu.RUnlock()
	m.scheduleEventMu.Lock()
	defer m.scheduleEventMu.Unlock()
	for kind, want := range needed {
		cancel, active := m.scheduleEventCancels[kind]
		if !want {
			if active && cancel != nil {
				cancel()
			}
			delete(m.scheduleEventCancels, kind)
			delete(m.scheduleEventErrors, kind)
			continue
		}
		if active || m.scheduleEventErrors[kind] != "" {
			continue
		}
		ctx, stop := context.WithCancel(context.Background())
		if err := startScheduleEventListener(ctx, kind, m.scheduleEvents); err != nil {
			stop()
			m.scheduleEventErrors[kind] = err.Error()
			m.markScheduleListenerUnavailable(kind, err)
			continue
		}
		m.scheduleEventCancels[kind] = stop
	}
}

func (m *Manager) markScheduleListenerUnavailable(kind string, listenerErr error) {
	toPersist := make([]*scheduleState, 0)
	m.schedulesMu.Lock()
	for _, schedule := range m.schedules {
		if schedule.Enabled && schedule.Type == kind {
			schedule.LastResult = fail("EVENT_LISTENER_UNAVAILABLE", "事件任务已持久化，但当前运行平台无法启动监听器", listenerErr, kind+"_event_listener")
			toPersist = append(toPersist, schedule)
		}
	}
	m.schedulesMu.Unlock()
	for _, schedule := range toPersist {
		_ = m.persistSchedule(schedule)
	}
}

func (m *Manager) scheduleListenerStatus() map[string]any {
	m.scheduleEventMu.Lock()
	defer m.scheduleEventMu.Unlock()
	out := make(map[string]any)
	for _, kind := range []string{"network", "charging"} {
		_, active := m.scheduleEventCancels[kind]
		out[kind] = map[string]any{"active": active, "error": m.scheduleEventErrors[kind]}
	}
	return out
}

func scheduleNext(schedule *scheduleState, after time.Time) (time.Time, error) {
	location := after.Location()
	switch schedule.Type {
	case "once":
		if schedule.At.After(after) {
			return schedule.At, nil
		}
		return time.Time{}, errors.New("一次性任务时间已过")
	case "interval":
		return after.Add(time.Duration(schedule.EverySeconds) * time.Second), nil
	case "daily", "weekly":
		parts := strings.Split(schedule.TimeOfDay, ":")
		hour, _ := strconv.Atoi(parts[0])
		minute, _ := strconv.Atoi(parts[1])
		next := time.Date(after.Year(), after.Month(), after.Day(), hour, minute, 0, 0, location)
		if !next.After(after) {
			next = next.Add(24 * time.Hour)
		}
		if schedule.Type == "weekly" {
			for int(next.Weekday()) != schedule.Weekday {
				next = next.Add(24 * time.Hour)
			}
		}
		return next, nil
	case "cron":
		return nextCronTime(schedule.Cron, after)
	case "boot":
		return after.Add(10 * time.Second), nil
	case "network", "charging":
		return time.Time{}, nil
	default:
		return time.Time{}, errors.New("未知定时任务类型")
	}
}

func nextCronTime(expression string, after time.Time) (time.Time, error) {
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return time.Time{}, errors.New("cron 必须是 5 字段：分 时 日 月 周")
	}
	candidate := after.Truncate(time.Minute).Add(time.Minute)
	limit := candidate.AddDate(5, 0, 0)
	for candidate.Before(limit) {
		values := []int{candidate.Minute(), candidate.Hour(), candidate.Day(), int(candidate.Month()), int(candidate.Weekday())}
		ranges := [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
		match := true
		for index, field := range fields {
			ok, err := cronFieldMatches(field, values[index], ranges[index][0], ranges[index][1])
			if err != nil {
				return time.Time{}, fmt.Errorf("cron 字段 %d: %w", index+1, err)
			}
			if !ok {
				match = false
				break
			}
		}
		if match {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, errors.New("未来 5 年内没有匹配的 cron 时间")
}

func cronFieldMatches(field string, value, minimum, maximum int) (bool, error) {
	for _, part := range strings.Split(field, ",") {
		step := 1
		base := part
		if index := strings.IndexByte(part, '/'); index >= 0 {
			base = part[:index]
			parsed, err := strconv.Atoi(part[index+1:])
			if err != nil || parsed <= 0 {
				return false, errors.New("步长必须是正整数")
			}
			step = parsed
		}
		start, end := minimum, maximum
		if base != "*" {
			if index := strings.IndexByte(base, '-'); index >= 0 {
				var err error
				start, err = strconv.Atoi(base[:index])
				if err != nil {
					return false, errors.New("范围起点无效")
				}
				end, err = strconv.Atoi(base[index+1:])
				if err != nil {
					return false, errors.New("范围终点无效")
				}
			} else {
				parsed, err := strconv.Atoi(base)
				if err != nil {
					return false, errors.New("字段必须是数字、*、范围、列表或步长")
				}
				start, end = parsed, parsed
			}
		}
		if start < minimum || end > maximum || start > end {
			return false, errors.New("字段超出允许范围")
		}
		if value >= start && value <= end && (value-start)%step == 0 {
			return true, nil
		}
	}
	return false, nil
}

func validTimeOfDay(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return false
	}
	hour, err1 := strconv.Atoi(parts[0])
	minute, err2 := strconv.Atoi(parts[1])
	return err1 == nil && err2 == nil && hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59
}

func safeScheduleID(id string) (string, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`+"\x00\r\n\t ") {
		return "", errors.New("定时任务 ID 不合法")
	}
	return id, nil
}

func (m *Manager) schedulePath(id string) string {
	id, _ = safeScheduleID(id)
	return filepath.Join(m.cfg.StateDir, "schedules", id+".json")
}

func (m *Manager) persistSchedule(schedule *scheduleState) error {
	path := m.schedulePath(schedule.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(scheduleSnapshot(schedule), "", "  ")
	if err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func scheduleSnapshot(schedule *scheduleState) scheduleView {
	return scheduleView(*schedule)
}

func parseNetlinkRouteEvent(data []byte) bool {
	for offset := 0; offset+16 <= len(data); {
		length := int(binary.NativeEndian.Uint32(data[offset : offset+4]))
		if length < 16 || offset+length > len(data) {
			return false
		}
		messageType := binary.NativeEndian.Uint16(data[offset+4 : offset+6])
		switch messageType {
		case 16, 20, 24: // RTM_NEWLINK, RTM_NEWADDR, RTM_NEWROUTE
			return true
		}
		offset += (length + 3) &^ 3
	}
	return false
}

func parseUEvent(data []byte) map[string]string {
	values := make(map[string]string)
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && strings.Contains(parts[0], "@") {
		values["DEVPATH_EVENT"] = parts[0]
	}
	for _, part := range parts[1:] {
		if index := strings.IndexByte(part, '='); index > 0 {
			values[part[:index]] = part[index+1:]
		}
	}
	return values
}

func ueventIndicatesCharging(data []byte) bool {
	values := parseUEvent(data)
	if values["SUBSYSTEM"] != "power_supply" {
		return false
	}
	status := strings.ToLower(values["POWER_SUPPLY_STATUS"])
	return status == "charging" || status == "full" || values["POWER_SUPPLY_ONLINE"] == "1"
}

func hasActiveNetwork() bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, item := range interfaces {
		if item.Flags&net.FlagUp == 0 || item.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := item.Addrs()
		if len(addresses) > 0 {
			return true
		}
	}
	return false
}
