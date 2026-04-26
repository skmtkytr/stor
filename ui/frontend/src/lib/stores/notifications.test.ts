// Side-effect import installs $state / $derived globals before
// notifications.svelte module evaluation. Order matters: bun ESM evaluates
// imports in source order.
import "./test-svelte-runes-shim";
import { describe, expect, test, beforeEach } from "bun:test";
import { notifications } from "./notifications.svelte";

beforeEach(() => {
	notifications.clearHistory();
	notifications.closePanel();
});

describe("ActivityLogStore", () => {
	test("push appends to history (newest first)", () => {
		notifications.push("info", "first");
		notifications.push("info", "second");
		expect(notifications.history).toHaveLength(2);
		expect(notifications.history[0].title).toBe("second");
		expect(notifications.history[1].title).toBe("first");
	});

	test("history is capped at 200 entries", () => {
		for (let i = 0; i < 250; i++) {
			notifications.push("info", `t${i}`);
		}
		expect(notifications.history).toHaveLength(200);
		expect(notifications.history[0].title).toBe("t249");
		expect(notifications.history[199].title).toBe("t50");
	});

	test("clearHistory empties the log", () => {
		notifications.push("warn", "x");
		notifications.clearHistory();
		expect(notifications.history).toHaveLength(0);
	});

	test("openPanel marks all unread as read", () => {
		notifications.push("warn", "a");
		notifications.push("warn", "b");
		expect(notifications.unreadCount).toBe(2);
		notifications.openPanel();
		expect(notifications.unreadCount).toBe(0);
		expect(notifications.history.every((n) => n.read)).toBe(true);
	});

	test("togglePanel flips state and reads", () => {
		notifications.push("info", "x");
		expect(notifications.panelOpen).toBe(false);
		expect(notifications.unreadCount).toBe(1);
		notifications.togglePanel();
		expect(notifications.panelOpen).toBe(true);
		expect(notifications.unreadCount).toBe(0);
		notifications.togglePanel();
		expect(notifications.panelOpen).toBe(false);
	});

	test("new push after openPanel re-introduces unread", () => {
		notifications.push("warn", "a");
		notifications.openPanel();
		expect(notifications.unreadCount).toBe(0);
		notifications.push("error", "b");
		expect(notifications.unreadCount).toBe(1);
	});

	test("body is preserved", () => {
		notifications.push("error", "title", "body text");
		expect(notifications.history[0].body).toBe("body text");
	});

	test("kind is preserved", () => {
		notifications.push("success", "ok");
		expect(notifications.history[0].kind).toBe("success");
	});
});
