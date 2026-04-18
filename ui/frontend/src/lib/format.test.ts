import { describe, expect, test } from "bun:test";
import { formatBytes, formatSpeed, formatETA, formatRatio, formatUnixDate } from "./format";

describe("formatBytes", () => {
	test("bytes", () => {
		expect(formatBytes(0)).toBe("0 B");
		expect(formatBytes(512)).toBe("512 B");
		expect(formatBytes(1023)).toBe("1023 B");
	});

	test("kilobytes", () => {
		expect(formatBytes(1024)).toBe("1 KB");
		expect(formatBytes(1536)).toBe("2 KB");
		expect(formatBytes(10240)).toBe("10 KB");
	});

	test("megabytes", () => {
		expect(formatBytes(1048576)).toBe("1.0 MB");
		expect(formatBytes(1572864)).toBe("1.5 MB");
		expect(formatBytes(10485760)).toBe("10.0 MB");
	});

	test("gigabytes", () => {
		expect(formatBytes(1073741824)).toBe("1.00 GB");
		expect(formatBytes(1610612736)).toBe("1.50 GB");
		expect(formatBytes(4831838208)).toBe("4.50 GB");
	});
});

describe("formatSpeed", () => {
	test("appends /s", () => {
		expect(formatSpeed(0)).toBe("0 B/s");
		expect(formatSpeed(1048576)).toBe("1.0 MB/s");
		expect(formatSpeed(1073741824)).toBe("1.00 GB/s");
	});
});

describe("formatETA", () => {
	test("returns - when speed is 0", () => {
		expect(formatETA(0, 1000, 0)).toBe("-");
	});

	test("returns - when speed is negative", () => {
		expect(formatETA(0, 1000, -1)).toBe("-");
	});

	test("returns - when download is complete", () => {
		expect(formatETA(1000, 1000, 100)).toBe("-");
	});

	test("returns - when downloaded exceeds total", () => {
		expect(formatETA(1500, 1000, 100)).toBe("-");
	});

	test("seconds format", () => {
		expect(formatETA(0, 30, 1)).toBe("30s");
		expect(formatETA(0, 59, 1)).toBe("59s");
	});

	test("minutes format", () => {
		expect(formatETA(0, 120, 1)).toBe("2m 0s");
		expect(formatETA(0, 150, 1)).toBe("2m 30s");
		expect(formatETA(0, 3599, 1)).toBe("59m 59s");
	});

	test("hours format", () => {
		expect(formatETA(0, 3600, 1)).toBe("1h 0m");
		expect(formatETA(0, 7200, 1)).toBe("2h 0m");
		expect(formatETA(0, 5400, 1)).toBe("1h 30m");
	});

	test("partial download", () => {
		// 500 remaining, speed 100 => 5s
		expect(formatETA(500, 1000, 100)).toBe("5s");
	});
});

describe("formatRatio", () => {
	test("zero downloaded, zero uploaded", () => {
		expect(formatRatio(0, 0)).toBe("0.000");
	});

	test("zero downloaded, positive uploaded → infinity", () => {
		expect(formatRatio(0, 1024)).toBe("\u221e");
	});

	test("rounds to three decimals", () => {
		expect(formatRatio(1000, 1500)).toBe("1.500");
		expect(formatRatio(3, 1)).toBe("0.333");
	});

	test("equal values gives 1.000", () => {
		expect(formatRatio(2048, 2048)).toBe("1.000");
	});

	test("handles large values", () => {
		expect(formatRatio(1, 1_000_000)).toBe("1000000.000");
	});
});

describe("formatUnixDate", () => {
	test("returns - for zero timestamp", () => {
		expect(formatUnixDate(0)).toBe("-");
	});

	test("returns a non-empty string for a valid timestamp", () => {
		const s = formatUnixDate(1_700_000_000);
		expect(typeof s).toBe("string");
		expect(s.length).toBeGreaterThan(0);
		expect(s).not.toBe("-");
	});
});
