// Browser storage can be missing or blocked (private mode, strict settings);
// it only holds conveniences, so failures are ignored.

export function readLocal(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writeLocal(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // storage unavailable
  }
}

// sessionStorage lives as long as the tab, which is what "the truck being
// photographed right now" needs: it survives a reload but not a new session.

export function readSession(key: string): string | null {
  try {
    return sessionStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writeSession(key: string, value: string) {
  try {
    sessionStorage.setItem(key, value);
  } catch {
    // storage unavailable
  }
}

export function removeSession(key: string) {
  try {
    sessionStorage.removeItem(key);
  } catch {
    // storage unavailable
  }
}

let counter = 0;

// localId makes keys for list items. crypto.randomUUID needs HTTPS, which the
// LAN app does not have.
export function localId(): string {
  counter += 1;
  return `${Date.now().toString(36)}-${counter}`;
}
