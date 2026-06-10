// Package forwarder publishes decoded GT06 telemetry to a Redis Stream.
//
// Laravel integration:
//
//	GT06 Go server
//	  └── XADD gt06:telemetry * event location imei lat lon speed course …
//	                │
//	                ▼ Redis Stream
//	                │
//	  Laravel Queue Worker  (package-gt06: gt06:consume)
//	    └── SignalService::record()
//	          ├── Upsert device_locations
//	          ├── Evaluate geo-fence → Incident
//	          └── Broadcast via Soketi
package forwarder

import (
	"context"
	"encoding/json"
	"gt06-server/pkg/protocol"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Stream publishes telemetry to a Redis Stream.
type Stream struct {
	rdb       *redis.Client
	streamKey string
	maxLen    int64
	log       *zap.Logger
}

func NewStream(rdb *redis.Client, streamKey string, maxLen int64, log *zap.Logger) *Stream {
	return &Stream{rdb: rdb, streamKey: streamKey, maxLen: maxLen, log: log}
}

// PublishLocation publishes a decoded GPS location to the stream.
func (s *Stream) PublishLocation(ctx context.Context, imei string, loc *protocol.LocationReport) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	values := map[string]any{
		"event":        "location",
		"imei":         imei,
		"timestamp":    loc.Timestamp.Unix(),
		"speed":        loc.Speed,
		"course":       loc.Course,
		"gps_fixed":    boolInt(loc.GPSFixed),
		"acc_on":       boolInt(loc.ACCOn),
		"satellites":   loc.Satellites,
		"alarm_flags":  loc.AlarmType,
		"published_at": time.Now().UnixMilli(),
	}

	if loc.GPSFixed {
		values["latitude"] = loc.Latitude
		values["longitude"] = loc.Longitude
	}

	if loc.AlarmType != protocol.AlarmNone {
		values["active_alarms"] = alarmJSON(loc.AlarmType)
	} else {
		values["active_alarms"] = "[]"
	}

	args := &redis.XAddArgs{
		Stream: s.streamKey,
		MaxLen: s.maxLen,
		Approx: true,
		Values: values,
	}

	if id, err := s.rdb.XAdd(ctx, args).Result(); err != nil {
		s.log.Warn("stream publish failed", zap.String("imei", imei), zap.Error(err))
	} else {
		s.log.Info("location published",
			zap.String("imei", imei),
			zap.String("stream_id", id),
			zap.Bool("gps_fixed", loc.GPSFixed),
			zap.Float64("lat", loc.Latitude),
			zap.Float64("lon", loc.Longitude),
			zap.Float64("speed_kmh", loc.Speed),
		)
	}
}

// PublishStatus publishes a device status update (battery, signal) to the stream.
func (s *Stream) PublishStatus(ctx context.Context, imei string, status *protocol.StatusReport) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	args := &redis.XAddArgs{
		Stream: s.streamKey,
		MaxLen: s.maxLen,
		Approx: true,
		Values: map[string]any{
			"event":          "status",
			"imei":           imei,
			"voltage_level":  status.VoltageLevel,
			"battery_level":  protocol.VoltagePercent(status.VoltageLevel),
			"signal_strength": status.SignalStrength,
			"alarm_type":     status.AlarmType,
			"published_at":   time.Now().UnixMilli(),
		},
	}

	if _, err := s.rdb.XAdd(ctx, args).Result(); err != nil {
		s.log.Warn("status publish failed", zap.String("imei", imei), zap.Error(err))
	}
}

// PublishAlarm publishes an alarm event to the stream.
func (s *Stream) PublishAlarm(ctx context.Context, imei string, alarm *protocol.AlarmReport) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	values := map[string]any{
		"event":        "alarm",
		"imei":         imei,
		"alarm_type":   alarm.AlarmType,
		"alarm_name":   protocol.AlarmName(alarm.AlarmType),
		"published_at": time.Now().UnixMilli(),
	}

	if alarm.HasLocation && alarm.Location != nil {
		loc := alarm.Location
		if loc.GPSFixed {
			values["latitude"] = loc.Latitude
			values["longitude"] = loc.Longitude
		}
		values["speed"] = loc.Speed
		values["gps_fixed"] = boolInt(loc.GPSFixed)
	}

	args := &redis.XAddArgs{
		Stream: s.streamKey,
		MaxLen: s.maxLen,
		Approx: true,
		Values: values,
	}

	if _, err := s.rdb.XAdd(ctx, args).Result(); err != nil {
		s.log.Warn("alarm publish failed", zap.String("imei", imei), zap.Error(err))
	}
}

// PublishEvent publishes a generic lifecycle event (login, device created, etc.).
func (s *Stream) PublishEvent(ctx context.Context, event, imei string, payload map[string]any) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	payloadJSON, _ := json.Marshal(payload)
	args := &redis.XAddArgs{
		Stream: s.streamKey,
		MaxLen: s.maxLen,
		Approx: true,
		Values: map[string]any{
			"event":        event,
			"imei":         imei,
			"payload":      string(payloadJSON),
			"published_at": time.Now().UnixMilli(),
		},
	}

	if _, err := s.rdb.XAdd(ctx, args).Result(); err != nil {
		s.log.Warn("event publish failed", zap.String("event", event), zap.String("imei", imei), zap.Error(err))
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func alarmJSON(alarmType uint8) string {
	var names []string
	checks := []struct {
		flag uint8
		name string
	}{
		{protocol.AlarmSOS, "sos"},
		{protocol.AlarmPowerCut, "power_cut"},
		{protocol.AlarmVibration, "vibration"},
		{protocol.AlarmEnterFence, "enter_fence"},
		{protocol.AlarmExitFence, "exit_fence"},
		{protocol.AlarmOverspeed, "overspeed"},
		{protocol.AlarmLowBattery, "low_battery"},
	}
	for _, c := range checks {
		if alarmType&c.flag != 0 {
			names = append(names, c.name)
		}
	}
	if len(names) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(names)
	return string(b)
}
