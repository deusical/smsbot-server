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

  it("excludes unwritable individual conversations in either scope", () => {
    for (const scope of ["selected", "all"] as const)
      expect(inboxAllows({ ...settings, scope }, { ...conversation, writable: false })).toBe(false);
  });

  describe("groups", () => {
    const group: Conversation = { ...conversation, id: "synthetic-group", participant_id: "group:synthetic-group", group: true, writable: false };
    const chosen = { ...settings, conversations: [{ conversation: group.id, participant: group.participant_id }] };

    it("are allowed when picked by name, whatever the all-scope switch says", () => {
      expect(inboxAllows(chosen, group)).toBe(true);
      expect(inboxAllows(settings, group)).toBe(false);
      expect(inboxAllows(chosen, { ...group, participant_id: "group:other" })).toBe(false);
    });

    it("are part of all scope only when the switch is on", () => {
      const all = { ...settings, scope: "all" as const, conversations: [] };
      expect(inboxAllows(all, group)).toBe(false);
      expect(inboxAllows({ ...all, groups: true }, group)).toBe(true);
      expect(inboxAllows({ ...all, groups: true }, conversation)).toBe(true);
    });

    it("need an identity", () => {
      expect(inboxAllows({ ...settings, scope: "all", conversations: [], groups: true }, { ...group, participant_id: "" })).toBe(false);
    });
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
