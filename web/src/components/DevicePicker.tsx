interface DevicePickerProps {
  label: string;
  devices: MediaDeviceInfo[];
  value: string;
  onChange: (deviceId: string) => void;
}

export function DevicePicker({ label, devices, value, onChange }: DevicePickerProps) {
  if (devices.length === 0) return null;
  return (
    <select
      aria-label={label}
      className="form-input"
      style={{ width: "auto", maxWidth: 220 }}
      value={value}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="">Browser default</option>
      {devices.map((d) => (
        <option key={d.deviceId} value={d.deviceId}>{d.label}</option>
      ))}
    </select>
  );
}
