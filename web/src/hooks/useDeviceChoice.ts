import { useCallback, useEffect, useState } from "react";

// Chrome adds "default"/"communications" aliases; "Browser default" covers them.
const ALIASES = ["default", "communications"];

// Lists named input devices of one kind and remembers the user's pick.
// Names are only exposed after permission, so call refresh() once a stream opens.
export function useDeviceChoice(kind: MediaDeviceKind, storageKey: string) {
  const [devices, setDevices] = useState<MediaDeviceInfo[]>([]);
  const [chosenId, setChosenId] = useState(() => localStorage.getItem(storageKey) ?? "");

  const refresh = useCallback(() => {
    navigator.mediaDevices.enumerateDevices?.().then(
      (all) => setDevices(all.filter((d) => d.kind === kind && d.label && !ALIASES.includes(d.deviceId))),
      () => {},
    );
  }, [kind]);

  useEffect(refresh, [refresh]);

  const select = useCallback((id: string) => {
    setChosenId(id);
    localStorage.setItem(storageKey, id);
  }, [storageKey]);

  // A saved device that is no longer plugged in falls back to the browser default.
  const selectedId = devices.some((d) => d.deviceId === chosenId) ? chosenId : "";

  return { devices, selectedId, select, refresh };
}
