import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { Window } from "happy-dom";
import { IconSchema } from "../gen/widgetd/v1/stream_pb.ts";
import { widgetIcon } from "./icon.ts";

const doc = new Window().document as unknown as Document;

const cloudSun = () =>
  create(IconSchema, {
    name: "cloud-sun",
    shapes: [
      { tag: "path", attrs: { d: "M12 2v2" } },
      { tag: "circle", attrs: { cx: "12", cy: "12", r: ".5", fill: "currentColor" } },
      { tag: "polyline", attrs: { points: "22 12 18 12 15 21" } },
    ],
  });

describe("widgetIcon", () => {
  test("draws Lucide geometry as an <svg>", () => {
    const svg = widgetIcon(cloudSun(), 14, doc)!;
    expect(svg.namespaceURI).toBe("http://www.w3.org/2000/svg");
    expect(svg.getAttribute("viewBox")).toBe("0 0 24 24");
    expect(svg.getAttribute("width")).toBe("14");
    expect(svg.getAttribute("stroke")).toBe("currentColor");
    expect(svg.getAttribute("aria-hidden")).toBe("true");
    expect([...svg.children].map((c) => c.tagName.toLowerCase())).toEqual(["path", "circle", "polyline"]);
    expect(svg.children[0]!.getAttribute("d")).toBe("M12 2v2");
    expect(svg.children[1]!.getAttribute("fill")).toBe("currentColor");
  });

  test("no icon, no <svg>", () => {
    expect(widgetIcon(undefined, 16, doc)).toBeUndefined();
    expect(widgetIcon(create(IconSchema, { name: "x" }), 16, doc)).toBeUndefined();
  });

  // The host refuses all of these; the page refuses them again.
  const spoiled: Record<string, (i: ReturnType<typeof cloudSun>) => void> = {
    "a script": (i) => void (i.shapes[0]!.tag = "script"),
    "an image": (i) => void (i.shapes[0] = { ...i.shapes[0]!, tag: "image", attrs: { href: "x" } }),
    "an event handler": (i) => void (i.shapes[0]!.attrs.onload = "alert(1)"),
    "a style": (i) => void (i.shapes[0]!.attrs.style = "x"),
    "markup in geometry": (i) => void (i.shapes[0]!.attrs.d = `M0"/><script>`),
    "a fill colour": (i) => void (i.shapes[1]!.attrs.fill = "url(#x)"),
    "another shape's attribute": (i) => void (i.shapes[0]!.attrs.r = "1"),
  };
  for (const [what, spoil] of Object.entries(spoiled)) {
    test(`refuses ${what}`, () => {
      const ic = cloudSun();
      spoil(ic);
      expect(widgetIcon(ic, 16, doc)).toBeUndefined();
    });
  }
});
