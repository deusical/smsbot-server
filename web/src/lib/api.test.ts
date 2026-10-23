import { describe, expect, it } from "vitest";
import { inboxAllows, type Conversation, type InboxSettings } from "./api";

const conversation: Conversation = {
  id: "synthetic-chat", participant_id: "synthetic-peer", label: "Fixture",
  protocol: "RCS", group: false, writable: true, enrolled: false,
};
const settings: InboxSettings = {
  enabled: true, scope: "selected",
  conversations: [{ conversation: conversation.id, participant: conversation.participant_id }],
  load_older: false, automatic_replies: false, revision: 1,
};

describe("inbox eligibility", () => {
  it("requires both the selected conversation and its exact participant", () => {
    expect(inboxAllows(settings, conversation)).toBe(true);
    expect(inboxAllows(settings, { ...conversation, participant_id: "different-peer" })).toBe(false);
    expect(inboxAllows(settings, { ...conversation, id: "different-chat" })).toBe(false);
  });

  it.each(["selected", "all"] as const)("excludes groups and unwritable conversations in %s scope", scope => {
    const config = { ...settings, scope };
    expect(inboxAllows(config, { ...conversation, group: true })).toBe(false);
    expect(inboxAllows(config, { ...conversation, writable: false })).toBe(false);
  });

  it("requires enabled settings, even with a matching selection", () => {
    expect(inboxAllows(undefined, conversation)).toBe(false);
    expect(inboxAllows(null, conversation)).toBe(false);
    expect(inboxAllows({ ...settings, enabled: false }, conversation)).toBe(false);
  });

  it("allows an unselected individual in all scope", () => {
    expect(inboxAllows({ ...settings, scope: "all", conversations: [] }, conversation)).toBe(true);
  });
});
