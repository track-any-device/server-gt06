package protocol

import (
	"encoding/binary"
	"fmt"
	"time"
)

// LoginInfo is decoded from a Proto 0x01 Login body.
type LoginInfo struct {
	IMEI string // 15-digit IMEI decoded from 8 BCD bytes
}

// LocationReport holds decoded GPS data from any location-bearing protocol.
type LocationReport struct {
	Timestamp  time.Time
	Latitude   float64
	Longitude  float64
	Speed      float64 // km/h
	Course     float64 // degrees 0–359
	GPSFixed   bool
	ACCOn      bool
	Satellites int
	AlarmType  uint8
}

// StatusReport holds decoded device status from Proto 0x11 / 0x17.
type StatusReport struct {
	VoltageLevel  int  // 0=none, 1=very low … 5=very high, 6=external power
	SignalStrength int  // 0–4 GSM bars
	AlarmType     uint8
}

// AlarmReport holds a decoded alarm event (0x16, 0x26, 0x28).
type AlarmReport struct {
	AlarmType   uint8
	HasLocation bool
	Location    *LocationReport
}

// DecodeLogin decodes a Proto 0x01 Login body.
// The first 8 bytes are the IMEI in packed BCD (15 digits + 0xF padding nibble).
func DecodeLogin(body []byte) (*LoginInfo, error) {
	if len(body) < 8 {
		return nil, fmt.Errorf("gt06: login body too short: %d (need 8)", len(body))
	}
	return &LoginInfo{IMEI: decodeBCDIMEI(body[:8])}, nil
}

// DecodeGPSLocation decodes a Proto 0x10 (or similar full-GPS) body.
//
// Body layout (18 bytes):
//
//	[0]     Year   (BCD, e.g. 0x24 = 2024)
//	[1]     Month  (BCD, 01–12)
//	[2]     Day    (BCD, 01–31)
//	[3]     Hour   (BCD, 00–23)
//	[4]     Minute (BCD, 00–59)
//	[5]     Second (BCD, 00–59)
//	[6]     GPS info: upper nibble = satellite count, lower nibble = data length indicator
//	[7:11]  Latitude  (uint32 BE, decimal degrees × 30000)
//	[11:15] Longitude (uint32 BE, decimal degrees × 30000)
//	[15]    Speed     (uint8, km/h)
//	[16:18] Course+Status (uint16 BE)
//	          bits  9:0  = course degrees (0–359)
//	          bit  10    = 1 → South latitude
//	          bit  11    = 1 → West longitude
//	          bit  12    = 1 → GPS fix valid
//	          bit  13    = 1 → real-time GPS
//	          bit  15    = 1 → ACC / ignition on
func DecodeGPSLocation(body []byte) (*LocationReport, error) {
	if len(body) < 18 {
		return nil, fmt.Errorf("gt06: GPS location body too short: %d (need 18)", len(body))
	}
	return decodeGPSAt(body, 0)
}

// DecodeGPSLBS decodes a Proto 0x1A body (GPS + LBS data).
// The GPS portion is identical to 0x10; the trailing LBS bytes are ignored here.
func DecodeGPSLBS(body []byte) (*LocationReport, error) {
	if len(body) < 18 {
		return nil, fmt.Errorf("gt06: GPS+LBS body too short: %d (need 18)", len(body))
	}
	return decodeGPSAt(body, 0)
}

// DecodeGPSLBSStatus decodes a Proto 0x19 body (GPS + LBS + Status combo).
// GPS portion is at offset 0; identical layout to 0x10.
func DecodeGPSLBSStatus(body []byte) (*LocationReport, error) {
	if len(body) < 18 {
		return nil, fmt.Errorf("gt06: GPS+LBS+Status body too short: %d (need 18)", len(body))
	}
	return decodeGPSAt(body, 0)
}

// DecodeGPSNetLBS decodes a Proto 0x22 body (GPS + Network + LBS).
// GPS portion starts at offset 0.
func DecodeGPSNetLBS(body []byte) (*LocationReport, error) {
	if len(body) < 18 {
		return nil, fmt.Errorf("gt06: GPS+Net+LBS body too short: %d (need 18)", len(body))
	}
	return decodeGPSAt(body, 0)
}

// DecodeGPSLBSExt decodes a Proto 0x2A body (GPS + LBS extended).
func DecodeGPSLBSExt(body []byte) (*LocationReport, error) {
	if len(body) < 18 {
		return nil, fmt.Errorf("gt06: GPS+LBS ext body too short: %d (need 18)", len(body))
	}
	return decodeGPSAt(body, 0)
}

// DecodeBatch decodes a Proto 0x25 Batch body.
// Returns one LocationReport per item with a valid GPS fix.
func DecodeBatch(body []byte) ([]*LocationReport, error) {
	if len(body) < 3 {
		return nil, fmt.Errorf("gt06: batch body too short: %d", len(body))
	}
	count := int(binary.BigEndian.Uint16(body[0:2]))
	// body[2] = batch type: 0x01 = GPS, 0x02 = LBS
	offset := 3
	results := make([]*LocationReport, 0, count)
	for i := 0; i < count; i++ {
		if offset+2 > len(body) {
			break
		}
		itemLen := int(binary.BigEndian.Uint16(body[offset : offset+2]))
		offset += 2
		if offset+itemLen > len(body) {
			break
		}
		item := body[offset : offset+itemLen]
		offset += itemLen

		if itemLen < 18 {
			continue
		}
		loc, err := decodeGPSAt(item, 0)
		if err != nil {
			continue
		}
		results = append(results, loc)
	}
	return results, nil
}

// DecodeStatus decodes a Proto 0x11 Status body (5 bytes).
//
//	[0] Terminal info byte (charging, ACC state flags)
//	[1] Voltage level (0–6)
//	[2] GSM signal strength (0–4)
//	[3:5] Alarm + Status flags (uint16)
func DecodeStatus(body []byte) (*StatusReport, error) {
	if len(body) < 5 {
		return nil, fmt.Errorf("gt06: status body too short: %d (need 5)", len(body))
	}
	return &StatusReport{
		VoltageLevel:  int(body[1]),
		SignalStrength: signalToPercent(int(body[2])),
		AlarmType:     body[3],
	}, nil
}

// DecodeBattery decodes a Proto 0x17 Battery body (2 bytes).
func DecodeBattery(body []byte) (*StatusReport, error) {
	if len(body) < 2 {
		return nil, fmt.Errorf("gt06: battery body too short: %d (need 2)", len(body))
	}
	return &StatusReport{
		VoltageLevel:  int(body[0]),
		SignalStrength: signalToPercent(int(body[1])),
	}, nil
}

// DecodeLBSAlarm decodes a Proto 0x16 or 0x26 alarm body.
// The alarm type byte is typically the last byte of the body.
func DecodeLBSAlarm(body []byte) (*AlarmReport, error) {
	if len(body) < 1 {
		return nil, fmt.Errorf("gt06: LBS alarm body empty")
	}
	alarmType := body[len(body)-1]
	return &AlarmReport{AlarmType: alarmType, HasLocation: false}, nil
}

// DecodeSpeedAlarm decodes a Proto 0x28 Speed Alarm body.
// The body is identical to a GPS Location (0x10) with the overspeed flag set.
func DecodeSpeedAlarm(body []byte) (*AlarmReport, error) {
	if len(body) < 18 {
		return &AlarmReport{AlarmType: AlarmOverspeed, HasLocation: false}, nil
	}
	loc, err := decodeGPSAt(body, 0)
	if err != nil {
		return &AlarmReport{AlarmType: AlarmOverspeed, HasLocation: false}, nil
	}
	loc.AlarmType = AlarmOverspeed
	return &AlarmReport{AlarmType: AlarmOverspeed, HasLocation: true, Location: loc}, nil
}

// AlarmName returns a human-readable name for an alarm type constant.
func AlarmName(a uint8) string {
	switch a {
	case AlarmSOS:
		return "sos"
	case AlarmPowerCut:
		return "power_cut"
	case AlarmVibration:
		return "vibration"
	case AlarmEnterFence:
		return "enter_fence"
	case AlarmExitFence:
		return "exit_fence"
	case AlarmOverspeed:
		return "overspeed"
	case AlarmLowBattery:
		return "low_battery"
	default:
		return "unknown"
	}
}

// VoltagePercent maps voltage level (0–6) to an approximate battery percentage.
func VoltagePercent(level int) int {
	switch level {
	case 0:
		return 0
	case 1:
		return 10
	case 2:
		return 30
	case 3:
		return 50
	case 4:
		return 70
	case 5:
		return 90
	case 6:
		return 100 // external power / AC adapter
	default:
		return -1
	}
}

// ── internal helpers ──────────────────────────────────────────────────────────

// decodeGPSAt reads 18 bytes of GPS data starting at offset in body.
func decodeGPSAt(body []byte, offset int) (*LocationReport, error) {
	if len(body) < offset+18 {
		return nil, fmt.Errorf("gt06: GPS data at offset %d requires %d bytes, have %d",
			offset, offset+18, len(body))
	}
	b := body[offset:]

	year := int(bcdByte(b[0])) + 2000
	month := time.Month(bcdByte(b[1]))
	day := int(bcdByte(b[2]))
	hour := int(bcdByte(b[3]))
	min := int(bcdByte(b[4]))
	sec := int(bcdByte(b[5]))
	ts := time.Date(year, month, day, hour, min, sec, 0, time.UTC)

	sats := int(b[6] >> 4)

	latRaw := binary.BigEndian.Uint32(b[7:11])
	lonRaw := binary.BigEndian.Uint32(b[11:15])
	speed := float64(b[15])

	status := binary.BigEndian.Uint16(b[16:18])
	course := float64(status & 0x03FF)
	isSouth := (status>>statusBitSouth)&1 == 1
	isWest := (status>>statusBitWest)&1 == 1
	gpsFixed := (status>>statusBitGPSFix)&1 == 1
	accOn := (status>>statusBitACC)&1 == 1

	lat := float64(latRaw) / 30000.0
	lon := float64(lonRaw) / 30000.0
	if isSouth {
		lat = -lat
	}
	if isWest {
		lon = -lon
	}

	return &LocationReport{
		Timestamp:  ts,
		Latitude:   lat,
		Longitude:  lon,
		Speed:      speed,
		Course:     course,
		GPSFixed:   gpsFixed,
		ACCOn:      accOn,
		Satellites: sats,
	}, nil
}

// decodeBCDIMEI decodes 8 BCD bytes into a 15-digit IMEI string.
// Each byte encodes two decimal digits (high nibble first).
// The 16th nibble is 0xF padding and is dropped.
func decodeBCDIMEI(b []byte) string {
	digits := make([]byte, 0, 15)
	for _, v := range b {
		hi := (v >> 4) & 0x0F
		lo := v & 0x0F
		if hi <= 9 {
			digits = append(digits, '0'+hi)
		}
		if lo <= 9 {
			digits = append(digits, '0'+lo)
		}
	}
	if len(digits) > 15 {
		digits = digits[:15]
	}
	return string(digits)
}

// bcdByte decodes a single BCD byte to an integer (e.g. 0x23 → 23).
func bcdByte(b byte) byte {
	return (b>>4)*10 + (b & 0x0F)
}

// signalToPercent maps GT06 GSM signal strength (0–4) to a 0–100 percentage.
func signalToPercent(raw int) int {
	if raw < 0 {
		return 0
	}
	if raw > 4 {
		raw = 4
	}
	return raw * 25
}
