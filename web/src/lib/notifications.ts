import { api, ApiError } from "./api";

/** Per-device notification settings, as stored by the server. */
export type NotificationPrefs = {
  messages: "all" | "bot_on" | "chosen" | "off";
  conversations: string[];
  muted: string[];
  show_text: boolean;
  approvals: boolean;
  problems: boolean;
  connection: boolean;
};

export type NotificationDevice = {
  id: string;
  label: string;
  created: string;
  prefs: NotificationPrefs;
};

export type NotificationState = {
  public_key: string;
  devices: NotificationDevice[];
};

/** Why this browser cannot receive notifications, or "" when it can. */
export function pushUnsupported(): string {
  if (!window.isSecureContext) return "Notifications need the dashboard opened over HTTPS.";
  if (!("serviceWorker" in navigator) || !("PushManager" in window) || !("Notification" in window))
    return "This browser does not support notifications. On an iPhone or iPad, add SmsBot to the Home Screen first.";
  return "";
}

function keyBytes(base64url: string): Uint8Array {
  const padded = (base64url + "=".repeat((4 - (base64url.length % 4)) % 4)).replace(/-/g, "+").replace(/_/g, "/");
  return Uint8Array.from(atob(padded), (c) => c.charCodeAt(0));
}

/** A readable default name for this device, such as "Chrome on Android". */
export function deviceName(): string {
  const ua = navigator.userAgent;
  const browser = /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox" : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "Browser";
  const os = /Android/.test(ua) ? "Android" : /iPhone|iPad/.test(ua) ? "iOS" : /Windows/.test(ua) ? "Windows" : /Mac OS/.test(ua) ? "macOS" : /Linux/.test(ua) ? "Linux" : "this device";
  return `${browser} on ${os}`;
}

async function registration(): Promise<ServiceWorkerRegistration> {
  const existing = await navigator.serviceWorker.getRegistration();
  if (existing) return existing;
  return navigator.serviceWorker.register("/sw.js");
}

/**
 * Registers this browser and returns its device record. Registering again (for
 * example after the browser renewed its push keys) keeps the device's settings.
 */
export async function enableThisDevice(publicKey: string, label: string): Promise<NotificationDevice> {
  const permission = await Notification.requestPermission();
  if (permission !== "granted") throw new Error(permission === "denied" ? "notifications_blocked" : "notifications_dismissed");
  const reg = await registration();
  await navigator.serviceWorker.ready;
  let sub = await reg.pushManager.getSubscription();
  const key = keyBytes(publicKey);
  // A subscription made for another server key cannot be reused.
  const current = sub?.options.applicationServerKey;
  if (sub && current && new Uint8Array(current).join() !== key.join()) {
    await sub.unsubscribe();
    sub = null;
  }
  sub ??= await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key as BufferSource });
  const json = sub.toJSON();
  return api<NotificationDevice>("/notifications/devices", "POST", {
    endpoint: json.endpoint,
    keys: { p256dh: json.keys?.p256dh, auth: json.keys?.auth },
    label,
  });
}

/**
 * Finds which registered device is this browser, refreshing its keys. Null
 * when this browser has no subscription or its device was removed.
 */
export async function thisDevice(): Promise<NotificationDevice | null> {
  // A live subscription already proves permission; browsers drop it if the
  // permission is revoked.
  if (pushUnsupported()) return null;
  const reg = await navigator.serviceWorker.getRegistration();
  const sub = await reg?.pushManager.getSubscription();
  if (!sub) return null;
  const json = sub.toJSON();
  try {
    return await api<NotificationDevice>("/notifications/devices", "POST", {
      endpoint: json.endpoint,
      keys: { p256dh: json.keys?.p256dh, auth: json.keys?.auth },
      label: "",
      existing_only: true,
    });
  } catch (e) {
    // Removed from another device: stay off until enabled here again.
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

/** Stops notifications here: the browser forgets the subscription too. */
export async function disableThisDevice(id: string): Promise<void> {
  await api(`/notifications/devices/${encodeURIComponent(id)}`, "DELETE");
  const reg = await navigator.serviceWorker.getRegistration();
  await (await reg?.pushManager.getSubscription())?.unsubscribe();
}
