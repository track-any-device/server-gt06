// Package handler dispatches GT06 frames to the correct message handler.
//
// Device lifecycle:
//
//  1. TCP connect → StateConnected
//  2. 0x01 Login (IMEI) → MySQL CheckOrCreate → ACK → StateLoggedIn
//  3. 0x10 / 0x1A / 0x19 / 0x22 … GPS reports → stream publish → ACK
//  4. 0x13 Heartbeat → Redis TTL refresh → ACK
//  5. Disconnect / timeout → goroutine cleanup
//
// All frames received before StateLoggedIn (except 0x01) are discarded.
package handler

import (
	"context"
	"fmt"
	"gt06-server/pkg/protocol"
	"gt06-server/server/internal/config"
	"gt06-server/server/internal/forwarder"
	"gt06-server/server/internal/metrics"
	"gt06-server/server/internal/session"
	"gt06-server/server/internal/store"
	"time"

	"go.uber.org/zap"
)

// Handler dispatches GT06 frames to message-specific logic.
type Handler struct {
	cfg     *config.Config
	reg     *session.Registry
	fwd     *forwarder.Stream
	devices *store.DeviceStore
	metrics *metrics.Metrics
	log     *zap.Logger
}

func New(
	cfg *config.Config,
	reg *session.Registry,
	fwd *forwarder.Stream,
	devices *store.DeviceStore,
	m *metrics.Metrics,
	log *zap.Logger,
) *Handler {
	return &Handler{cfg: cfg, reg: reg, fwd: fwd, devices: devices, metrics: m, log: log}
}

// Dispatch routes an incoming frame to its handler.
func (h *Handler) Dispatch(ctx context.Context, s *session.Session, f *protocol.Frame) {
	h.metrics.FramesReceived.WithLabelValues(protocol.ProtoName(f.Protocol)).Inc()

	// Only Login is allowed before the device is logged in.
	if s.State() < session.StateLoggedIn && f.Protocol != protocol.ProtoLogin {
		h.log.Debug("pre-login frame discarded",
			zap.String("addr", s.RemoteAddr()),
			zap.String("proto", fmt.Sprintf("0x%02X", f.Protocol)),
		)
		return
	}

	switch f.Protocol {
	case protocol.ProtoLogin:
		h.handleLogin(ctx, s, f)
	case protocol.ProtoHeartbeat:
		h.handleHeartbeat(ctx, s, f)
	case protocol.ProtoGPSLocation, protocol.ProtoGPSLBSQuery:
		h.handleGPSLocation(ctx, s, f)
	case protocol.ProtoGPSLBS:
		h.handleGPSLBS(ctx, s, f)
	case protocol.ProtoGPSLBSStatus:
		h.handleGPSLBSStatus(ctx, s, f)
	case protocol.ProtoGPSNetLBS:
		h.handleGPSNetLBS(ctx, s, f)
	case protocol.ProtoGPSLBSExt:
		h.handleGPSLBSExt(ctx, s, f)
	case protocol.ProtoBatch:
		h.handleBatch(ctx, s, f)
	case protocol.ProtoStatus:
		h.handleStatus(ctx, s, f)
	case protocol.ProtoBattery:
		h.handleBattery(ctx, s, f)
	case protocol.ProtoLBSAlarm, protocol.ProtoLBSAlarm2:
		h.handleLBSAlarm(ctx, s, f)
	case protocol.ProtoSpeedAlarm:
		h.handleSpeedAlarm(ctx, s, f)
	case protocol.ProtoUTCTime:
		s.WriteACK(f.Protocol, f.Serial, h.cfg.WriteTimeout) //nolint:errcheck
	case protocol.ProtoOnlineCmdResp:
		h.log.Debug("online command response", zap.String("imei", s.IMEI), zap.Uint16("serial", f.Serial))
	case protocol.ProtoWiFi:
		h.log.Debug("WiFi info received (no coordinates, not published)",
			zap.String("imei", s.IMEI))
	default:
		h.log.Info("unhandled protocol",
			zap.String("imei", s.IMEI),
			zap.String("proto", fmt.Sprintf("0x%02X", f.Protocol)),
			zap.String("body_hex", fmt.Sprintf("%X", f.Body)),
		)
		h.metrics.UnknownMessages.Inc()
	}
}

func (h *Handler) handleLogin(ctx context.Context, s *session.Session, f *protocol.Frame) {
	info, err := protocol.DecodeLogin(f.Body)
	if err != nil {
		h.log.Warn("login decode error", zap.String("addr", s.RemoteAddr()), zap.Error(err))
		h.metrics.LoginFailure.Inc()
		h.metrics.DecodeErrors.Inc()
		return
	}

	result, dbErr := h.devices.CheckOrCreate(ctx, info.IMEI, "GT06")
	if dbErr != nil {
		h.log.Error("device check failed — rejecting", zap.String("imei", info.IMEI), zap.Error(dbErr))
		h.metrics.LoginFailure.Inc()
		time.AfterFunc(200*time.Millisecond, s.Close)
		return
	}

	switch result {
	case store.CheckBlocked:
		h.log.Warn("device blocked — rejecting", zap.String("broadcast_id", info.IMEI))
		h.metrics.LoginFailure.Inc()
		time.AfterFunc(200*time.Millisecond, s.Close)
		return
	case store.CheckAutoCreated:
		// New device: registered as pending (broadcast id only). Allow it to connect so
		// realtime broadcasts flow; package-gt06 skips signal storage until activated.
		h.log.Info("new device auto-created as pending — allowing", zap.String("broadcast_id", info.IMEI))
	}

	s.IMEI = info.IMEI
	s.SetState(session.StateLoggedIn)
	h.reg.Register(ctx, s)

	if err := s.WriteACK(f.Protocol, f.Serial, h.cfg.WriteTimeout); err != nil {
		h.log.Warn("login ACK write error", zap.String("imei", s.IMEI), zap.Error(err))
	}

	h.metrics.LoginSuccess.Inc()
	h.log.Info("device logged in", zap.String("imei", s.IMEI), zap.String("addr", s.RemoteAddr()))

	go h.fwd.PublishEvent(ctx, "device.login", s.IMEI, map[string]any{
		"imei":     s.IMEI,
		"addr":     s.RemoteAddr(),
		"login_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *Handler) handleHeartbeat(ctx context.Context, s *session.Session, f *protocol.Frame) {
	h.reg.Heartbeat(ctx, s)
	if err := s.WriteACK(f.Protocol, f.Serial, h.cfg.WriteTimeout); err != nil {
		h.log.Debug("heartbeat ACK error", zap.String("imei", s.IMEI), zap.Error(err))
	}
	h.metrics.Heartbeats.Inc()
	h.devices.RecordHeartbeat(ctx, s.IMEI)
	// Publish a keep-alive event so the consumer can forward "device alive" to tenants on a REST/MQTT
	// transport (no-op for tad101 / non-forwarding tenants).
	go h.fwd.PublishEvent(ctx, "heartbeat", s.IMEI, map[string]any{})
	h.log.Debug("heartbeat", zap.String("imei", s.IMEI))
}

func (h *Handler) handleGPSLocation(ctx context.Context, s *session.Session, f *protocol.Frame) {
	loc, err := protocol.DecodeGPSLocation(f.Body)
	if err != nil {
		h.log.Warn("GPS location decode error", zap.String("imei", s.IMEI), zap.Error(err))
		h.metrics.DecodeErrors.Inc()
		return
	}
	h.ackAndPublishLocation(ctx, s, f, loc)
}

func (h *Handler) handleGPSLBS(ctx context.Context, s *session.Session, f *protocol.Frame) {
	loc, err := protocol.DecodeGPSLBS(f.Body)
	if err != nil {
		h.log.Warn("GPS+LBS decode error", zap.String("imei", s.IMEI), zap.Error(err))
		h.metrics.DecodeErrors.Inc()
		return
	}
	h.ackAndPublishLocation(ctx, s, f, loc)
}

func (h *Handler) handleGPSLBSStatus(ctx context.Context, s *session.Session, f *protocol.Frame) {
	loc, err := protocol.DecodeGPSLBSStatus(f.Body)
	if err != nil {
		h.log.Warn("GPS+LBS+Status decode error", zap.String("imei", s.IMEI), zap.Error(err))
		h.metrics.DecodeErrors.Inc()
		return
	}
	h.ackAndPublishLocation(ctx, s, f, loc)
}

func (h *Handler) handleGPSNetLBS(ctx context.Context, s *session.Session, f *protocol.Frame) {
	loc, err := protocol.DecodeGPSNetLBS(f.Body)
	if err != nil {
		h.log.Warn("GPS+Net+LBS decode error", zap.String("imei", s.IMEI), zap.Error(err))
		h.metrics.DecodeErrors.Inc()
		return
	}
	h.ackAndPublishLocation(ctx, s, f, loc)
}

func (h *Handler) handleGPSLBSExt(ctx context.Context, s *session.Session, f *protocol.Frame) {
	loc, err := protocol.DecodeGPSLBSExt(f.Body)
	if err != nil {
		h.log.Warn("GPS+LBS ext decode error", zap.String("imei", s.IMEI), zap.Error(err))
		h.metrics.DecodeErrors.Inc()
		return
	}
	h.ackAndPublishLocation(ctx, s, f, loc)
}

func (h *Handler) handleBatch(ctx context.Context, s *session.Session, f *protocol.Frame) {
	items, err := protocol.DecodeBatch(f.Body)
	if err != nil {
		h.log.Warn("batch decode error", zap.String("imei", s.IMEI), zap.Error(err))
		h.metrics.DecodeErrors.Inc()
		return
	}
	s.WriteACK(f.Protocol, f.Serial, h.cfg.WriteTimeout) //nolint:errcheck
	for _, loc := range items {
		h.metrics.LocationReports.Inc()
		s.TouchLocation()
		go h.fwd.PublishLocation(ctx, s.IMEI, loc)
	}
	h.log.Debug("batch locations", zap.String("imei", s.IMEI), zap.Int("count", len(items)))
}

func (h *Handler) handleStatus(ctx context.Context, s *session.Session, f *protocol.Frame) {
	status, err := protocol.DecodeStatus(f.Body)
	if err != nil {
		h.log.Warn("status decode error", zap.String("imei", s.IMEI), zap.Error(err))
		h.metrics.DecodeErrors.Inc()
		return
	}
	go h.fwd.PublishStatus(ctx, s.IMEI, status)
}

func (h *Handler) handleBattery(ctx context.Context, s *session.Session, f *protocol.Frame) {
	status, err := protocol.DecodeBattery(f.Body)
	if err != nil {
		h.log.Warn("battery decode error", zap.String("imei", s.IMEI), zap.Error(err))
		h.metrics.DecodeErrors.Inc()
		return
	}
	go h.fwd.PublishStatus(ctx, s.IMEI, status)
}

func (h *Handler) handleLBSAlarm(ctx context.Context, s *session.Session, f *protocol.Frame) {
	alarm, err := protocol.DecodeLBSAlarm(f.Body)
	if err != nil {
		h.log.Warn("LBS alarm decode error", zap.String("imei", s.IMEI), zap.Error(err))
		h.metrics.DecodeErrors.Inc()
		return
	}
	if alarm.AlarmType == protocol.AlarmSOS {
		h.metrics.SOSAlarms.Inc()
		h.log.Warn("SOS ALARM (LBS)", zap.String("imei", s.IMEI))
	}
	go h.fwd.PublishAlarm(ctx, s.IMEI, alarm)
}

func (h *Handler) handleSpeedAlarm(ctx context.Context, s *session.Session, f *protocol.Frame) {
	alarm, err := protocol.DecodeSpeedAlarm(f.Body)
	if err != nil {
		h.log.Warn("speed alarm decode error", zap.String("imei", s.IMEI), zap.Error(err))
		h.metrics.DecodeErrors.Inc()
		return
	}
	h.metrics.OverspeedAlarms.Inc()
	s.WriteACK(f.Protocol, f.Serial, h.cfg.WriteTimeout) //nolint:errcheck
	go h.fwd.PublishAlarm(ctx, s.IMEI, alarm)
}

// ackAndPublishLocation is the common path for all location-bearing protocols.
func (h *Handler) ackAndPublishLocation(
	ctx context.Context,
	s *session.Session,
	f *protocol.Frame,
	loc *protocol.LocationReport,
) {
	if err := s.WriteACK(f.Protocol, f.Serial, h.cfg.WriteTimeout); err != nil {
		h.log.Debug("location ACK error", zap.String("imei", s.IMEI), zap.Error(err))
		return
	}

	if loc.AlarmType == protocol.AlarmSOS {
		h.metrics.SOSAlarms.Inc()
		h.log.Warn("SOS ALARM",
			zap.String("imei", s.IMEI),
			zap.Float64("lat", loc.Latitude),
			zap.Float64("lon", loc.Longitude),
		)
	}

	s.TouchLocation()
	h.metrics.LocationReports.Inc()
	go h.fwd.PublishLocation(ctx, s.IMEI, loc)
}
