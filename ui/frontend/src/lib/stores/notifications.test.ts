// Side-effect import installs $state / $derived globals before
// notifications.svelte module evaluation. Order matters: bun ESM evaluates
// imports in source order.
import "./test-svelte-runes-shim";
import { describe, expect, test, beforeEach } from "bun:test";
import { notifications } from "./notifications.svelte";

beforeEach(() => {
	notifications.clearHistory();
	notifications.dismissAllToasts();
	notifications.closePanel();
});

describe("NotificationStore", () => {
	test("push appends to both toasts and history", () => {
		notifications.push("info", "hello");
		expect(notifications.toasts).toHaveLength(1);
		expect(notifications.history).toHaveLength(1);
	});

	test("history is newest-first and capped at 200", () => {
		for (let i = 0; i < 250; i++) {
			notifications.push("info", `t${i}`);
		}
		expect(notifications.history).toHaveLength(200);
		expect(notifications.history[0].title).toBe("t249");
		expect(notifications.history[199].title).toBe("t50");
	});

	test("dismissToast removes from toasts but keeps history", () => {
		const id = notifications.push("info", "x");
		notifications.dismissToast(id);
		expect(notifications.toasts).toHaveLength(0);
		expect(notifications.history).toHaveLength(1);
		expect(notifications.history[0].id).toBe(id);
	});

	test("clearHistory empties history but does not touch toasts", () => {
		notifications.push("info", "x");
		notifications.clearHistory();
		expect(notifications.history).toHaveLength(0);
		expect(notifications.toasts).toHaveLength(1);
	});

	test("error notifications are not auto-dismissed (TTL = 0)", async () => {
		notifications.push("error", "boom");
		// Wait longer than the longest auto-dismiss window.
		await new Promise((r) => setTimeout(r, 50));
		expect(notifications.toasts).toHaveLength(1);
	});

	test("info notifications auto-dismiss; pass explicit short ttl to verify", async () => {
		notifications.push("info", "ephemeral", undefined, 10);
		await new Promise((r) => setTimeout(r, 30));
		expect(notifications.toasts).toHaveLength(0);
		// Still recorded in history.
		expect(notifications.history).toHaveLength(1);
	});

	test("explicit ttlMs overrides kind default", async () => {
		notifications.push("error", "soft", undefined, 10);
		await new Promise((r) => setTimeout(r, 30));
		expect(notifications.toasts).toHaveLength(0);
	});

	test("openPanel marks all unread as read", () => {
		notifications.push("warn", "a");
		notifications.push("warn", "b");
		expect(notifications.unreadCount).toBe(2);
		notifications.openPanel();
		expect(notifications.unreadCount).toBe(0);
		expect(notifications.history.every((n) => n.read)).toBe(true);
	});

	test("togglePanel flips state", () => {
		expect(notifications.panelOpen).toBe(false);
		notifications.togglePanel();
		expect(notifications.panelOpen).toBe(true);
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
});
