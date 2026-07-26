import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const playerSource = readFileSync(
  new URL("../src/components/VideoPlayer.tsx", import.meta.url),
  "utf8"
);

test("native stream seeks prioritize the new backend range", () => {
  assert.match(playerSource, /const STREAM_SEEK_PRIORITY_THROTTLE_MS = 250;/);
  assert.match(playerSource, /function streamSeekPriorityURL\(src: string\)/);
  assert.match(playerSource, /cleanSrc\.startsWith\("\/p\/stream\/"\)/);
  assert.match(playerSource, /`\/api\/stream-seek\/\$\{cleanSrc\.slice/);
  assert.match(
    playerSource,
    /video\.addEventListener\("seeking", handleVideoSeeking\)/
  );
  assert.match(
    playerSource,
    /video\.removeEventListener\("seeking", handleVideoSeeking\)/
  );
  assert.doesNotMatch(playerSource, /handleProgressPointerDown/);
  assert.match(
    playerSource,
    /fetch\(priorityURL, \{[\s\S]*?method: "POST"[\s\S]*?credentials: "same-origin"[\s\S]*?cache: "no-store"[\s\S]*?keepalive: true/s
  );
  assert.match(playerSource, /function streamSeekPlaybackReportURL\(/);
  assert.match(playerSource, /video\.addEventListener\("playing", reportStreamSeekPlayback\)/);
  assert.match(playerSource, /bufferedAheadSeconds\(\) \* 1000/);
});
