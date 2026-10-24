import { useCallback, useRef, useState } from "react";
import { api } from "./api";
import { clearSeen } from "./conversationOrder";
import { clearPending } from "./pendingSends";

/**
 * The browser session. The server confirms it (a successful state read) before
 * the dashboard shows; the generation lets every request started in an older
 * session discard its response, so nothing from a previous sign-in can
 * repopulate the next one.
 */
export function useSession() {
  const [signedIn, setSignedIn] = useState(false);
  // An installed home-screen app should stay signed in like any phone app.
  const [remember, setRemember] = useState(
    () => !!window.matchMedia?.("(display-mode: standalone)")?.matches,
  );
  const generation = useRef(0);
  /** The server accepted the session cookie. */
  const confirm = useCallback(() => setSignedIn(true), []);
  /** Ends the session locally and clears sensitive in-memory state. */
  const end = useCallback(() => {
    generation.current++;
    clearSeen();
    clearPending();
    setSignedIn(false);
  }, []);
  const signIn = useCallback(
    async (token: string) => {
      await api("/login", "POST", { token, remember });
      generation.current++;
    },
    [remember],
  );
  /** Revokes the server session; the caller then ends it locally. */
  const signOut = useCallback(async () => {
    await api("/logout", "POST");
  }, []);
  return { signedIn, remember, setRemember, generation, confirm, end, signIn, signOut };
}

export type Session = ReturnType<typeof useSession>;
