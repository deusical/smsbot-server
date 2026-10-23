import {useSyncExternalStore} from "react";

export type ReactionAttempt = {
  request_id: string;
  emoji: string;
  operation: "add" | "switch" | "remove";
  version: string;
  id?: string;
  state: string;
};
let epoch=0;
export const reactionEpoch=()=>epoch;
let attempts: Record<string, ReactionAttempt> = {};
const listeners = new Set<() => void>();
export const actionKey = (conversation: string, message: string) => `${conversation}:${message}`;
export function setReactionAttempt(key: string, attempt?: ReactionAttempt) {
  attempts = {...attempts};
  if (attempt) attempts[key] = attempt;
  else delete attempts[key];
  listeners.forEach(fn => fn());
}
export function clearReactionAttempts() {
  epoch++;
  attempts = {};
  listeners.forEach(fn => fn());
}
export const reactionAttempt = (key: string) => attempts[key];
export function useReactionAttempt(key: string) {
  return useSyncExternalStore(fn => {listeners.add(fn); return () => {listeners.delete(fn);};}, () => attempts[key]);
}
