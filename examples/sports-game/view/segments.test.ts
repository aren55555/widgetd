import { expect, test } from "bun:test";
import { digitCells, litSegments } from "./segments.ts";

test("litSegments", () => {
  const lit = (ch: string) => [...litSegments(ch)].sort().join("");
  expect(lit("8")).toBe("abcdefg");
  expect(lit("1")).toBe("bc");
  expect(lit("7")).toBe("abc");
  expect(lit(" ")).toBe("");
  expect(lit("x")).toBe("");
});

test("digitCells right-aligns with blanks", () => {
  expect(digitCells("7", 2)).toEqual([" ", "7"]);
  expect(digitCells("21", 2)).toEqual(["2", "1"]);
  expect(digitCells("104", 2)).toEqual(["1", "0", "4"]);
  expect(digitCells("98", 3)).toEqual([" ", "9", "8"]);
});
