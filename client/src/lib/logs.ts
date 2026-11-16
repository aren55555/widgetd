// Lines cards' views logged, held until the board sends them to the host in
// one SendLogs call. Frames are untrusted, so each line is checked and cut
// down here; the host checks again. Pure, so it is tested directly.

import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { LOG_LEVELS, type LogLevel } from "@widgetd/sdk/bridge";
import { ViewLog_Level, ViewLogSchema, type SendLogsRequest, type ViewLog } from "../gen/widgetd/v1/stream_pb.ts";

const levels: Record<LogLevel, ViewLog_Level> = {
  debug: ViewLog_Level.DEBUG,
  info: ViewLog_Level.INFO,
  warn: ViewLog_Level.WARN,
  error: ViewLog_Level.ERROR,
};

/** The host's own caps (host/viewlog.go), so nothing is sent only to be cut. */
export const LOG_LIMITS = { line: 4 << 10, attr: 1 << 10, attrs: 16, batch: 100 } as const;

export class LogQueue {
  private lines: ViewLog[] = [];
  private dropped = 0;

  /** `max` lines are held; past it, lines are counted and dropped. */
  constructor(private readonly max = 200) {}

  get size(): number {
    return this.lines.length;
  }

  /** Hold a frame's "log" message, if it is well formed, stamped with when it arrived (`now`, in ms). */
  push(cardId: string, m: { level?: unknown; message?: unknown; attrs?: unknown }, now = Date.now()): void {
    if (!LOG_LEVELS.includes(m.level as LogLevel) || typeof m.message !== "string") return;
    if (this.lines.length >= this.max) {
      this.dropped++;
      return;
    }
    const attrs: Record<string, string> = {};
    if (typeof m.attrs === "object" && m.attrs !== null) {
      let n = 0;
      for (const [k, v] of Object.entries(m.attrs)) {
        if (typeof v !== "string") continue;
        if (n++ >= LOG_LIMITS.attrs) break;
        attrs[k.slice(0, LOG_LIMITS.attr)] = v.slice(0, LOG_LIMITS.attr);
      }
    }
    this.lines.push(
      create(ViewLogSchema, {
        cardId,
        level: levels[m.level as LogLevel],
        message: m.message.slice(0, LOG_LIMITS.line),
        attrs,
        time: timestampFromMs(now),
      }),
    );
  }

  /** The next batch to send, oldest first; undefined when there's nothing to say. */
  take(): Pick<SendLogsRequest, "logs" | "dropped"> | undefined {
    if (this.lines.length === 0 && this.dropped === 0) return undefined;
    const logs = this.lines.splice(0, LOG_LIMITS.batch);
    const dropped = this.dropped;
    this.dropped = 0;
    return { logs, dropped };
  }
}
