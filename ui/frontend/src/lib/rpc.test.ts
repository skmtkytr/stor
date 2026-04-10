import { describe, expect, test, beforeEach } from "bun:test";
import { getApiKey, setApiKey, RPCError } from "./rpc";

describe("API key management", () => {
	beforeEach(() => {
		setApiKey("");
	});

	test("getApiKey returns empty string initially", () => {
		expect(getApiKey()).toBe("");
	});

	test("setApiKey stores and retrieves key", () => {
		setApiKey("sk-test-123");
		expect(getApiKey()).toBe("sk-test-123");
	});

	test("setApiKey with empty string clears key", () => {
		setApiKey("sk-test-123");
		setApiKey("");
		expect(getApiKey()).toBe("");
	});
});

describe("RPCError", () => {
	test("has code and message", () => {
		const err = new RPCError(404, "not found");
		expect(err.code).toBe(404);
		expect(err.message).toBe("not found");
	});

	test("is an instance of Error", () => {
		const err = new RPCError(500, "internal error");
		expect(err).toBeInstanceOf(Error);
		expect(err).toBeInstanceOf(RPCError);
	});
});
