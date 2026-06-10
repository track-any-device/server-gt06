package protocol

// Protocol numbers (message types) sent by the device.
const (
	ProtoLogin         uint8 = 0x01
	ProtoGPSLocation   uint8 = 0x10
	ProtoStatus        uint8 = 0x11
	ProtoOnlineCmdResp uint8 = 0x12
	ProtoHeartbeat     uint8 = 0x13
	ProtoGPSLBSQuery   uint8 = 0x14
	ProtoUTCTime       uint8 = 0x15
	ProtoLBSAlarm      uint8 = 0x16
	ProtoBattery       uint8 = 0x17
	ProtoGPSLBSStatus  uint8 = 0x19
	ProtoGPSLBS        uint8 = 0x1A
	ProtoGPSNetLBS     uint8 = 0x22
	ProtoBatch         uint8 = 0x25
	ProtoLBSAlarm2     uint8 = 0x26
	ProtoWiFi          uint8 = 0x27
	ProtoSpeedAlarm    uint8 = 0x28
	ProtoGPSLBSExt     uint8 = 0x2A
)

// Alarm type flags (Status.AlarmType field and alarm event payloads).
const (
	AlarmNone        uint8 = 0x00
	AlarmSOS         uint8 = 0x01
	AlarmPowerCut    uint8 = 0x02
	AlarmVibration   uint8 = 0x04
	AlarmEnterFence  uint8 = 0x08
	AlarmExitFence   uint8 = 0x10
	AlarmOverspeed   uint8 = 0x20
	AlarmLowBattery  uint8 = 0x40
)

// Course+Status word bit positions (2-byte field at end of GPS body).
const (
	statusBitSouth    = 10 // 1 = South latitude
	statusBitWest     = 11 // 1 = West longitude
	statusBitGPSFix   = 12 // 1 = GPS positioned
	statusBitRealTime = 13 // 1 = real-time GPS
	statusBitDiff     = 14 // 1 = differential GPS fix
	statusBitACC      = 15 // 1 = ACC / ignition on
)

// ProtoName returns a human-readable name for a protocol byte.
func ProtoName(p uint8) string {
	switch p {
	case ProtoLogin:
		return "login"
	case ProtoGPSLocation:
		return "gps_location"
	case ProtoStatus:
		return "status"
	case ProtoOnlineCmdResp:
		return "online_cmd_resp"
	case ProtoHeartbeat:
		return "heartbeat"
	case ProtoGPSLBSQuery:
		return "gps_lbs_query"
	case ProtoUTCTime:
		return "utc_time"
	case ProtoLBSAlarm:
		return "lbs_alarm"
	case ProtoBattery:
		return "battery"
	case ProtoGPSLBSStatus:
		return "gps_lbs_status"
	case ProtoGPSLBS:
		return "gps_lbs"
	case ProtoGPSNetLBS:
		return "gps_net_lbs"
	case ProtoBatch:
		return "batch"
	case ProtoLBSAlarm2:
		return "lbs_alarm2"
	case ProtoWiFi:
		return "wifi"
	case ProtoSpeedAlarm:
		return "speed_alarm"
	case ProtoGPSLBSExt:
		return "gps_lbs_ext"
	default:
		return "unknown"
	}
}
