import { ApiError, type Job } from "../api/client";
import {
  duration,
  explain,
  hasDetail,
  IDLE_REFRESH_MS,
  labelClass,
  refreshInterval,
  revertable,
  rows,
  RUNNING_REFRESH_MS,
  stillRunning,
} from "./rows";
import { byId } from "../tenants";

function job(fields: Partial<Job>): Job {
  return {
    id: "j1",
    kind: "MIGRATION",
    status: "SUCCEEDED",
    runId: "r1",
    ...fields,
  };
}

describe("the history list", () => {
  it("shows migrations and reverts together, newest first", () => {
    const list = rows(
      [job({ id: "older", queuedAt: "2026-08-01T10:00:00Z" })],
      [job({ id: "newer", kind: "REVERT", queuedAt: "2026-08-02T10:00:00Z" })],
    );

    expect(list.map((row) => row.job.id)).toEqual(["newer", "older"]);
  });

  it("offers to undo a finished migration that marked what it created", () => {
    expect(revertable(job({}))).toBe(true);
  });

  it("refuses to undo a run that marked nothing, because nothing could find it again", () => {
    expect(
      revertable(
        job({
          request: {
            sourceTenant: "t1",
            targetTenant: "t2",
            rootId: "1",
            noFlag: true,
          },
        }),
      ),
    ).toBe(false);
  });

  it("refuses to undo a run that never announced a run id", () => {
    expect(revertable(job({ runId: undefined, status: "FAILED" }))).toBe(false);
  });

  it("does not offer to undo an undo", () => {
    expect(revertable(job({ kind: "REVERT" }))).toBe(false);
  });

  it("waits for a running migration to finish before offering to undo it", () => {
    expect(revertable(job({ status: "RUNNING" }))).toBe(false);
    expect(revertable(job({ status: "QUEUED" }))).toBe(false);
  });

  it("offers to cancel exactly what is still running", () => {
    const list = rows(
      [job({ id: "a", status: "RUNNING" }), job({ id: "b", status: "FAILED" })],
      [],
    );

    expect(list.find((row) => row.job.id === "a")?.cancellable).toBe(true);
    expect(list.find((row) => row.job.id === "b")?.cancellable).toBe(false);
  });

  it("carries the approximate device count of a running job", () => {
    const list = rows(
      [job({ status: "RUNNING", progress: ["Device 3 of 9 (1399229)"] })],
      [],
    );

    expect(list[0].progress).toEqual({ current: 3, total: 9, percent: 33 });
  });

  // The line stays in the log after the job is over, so reading it from there
  // left a finished migration wearing a full progress bar and a caption under it.
  it("shows no progress on a job that has finished", () => {
    for (const status of [
      "SUCCEEDED",
      "FAILED",
      "CANCELLED",
      "INTERRUPTED",
    ] as const) {
      const list = rows(
        [job({ status, progress: ["Device 9 of 9 (1399229)"] })],
        [],
      );
      expect(list[0].progress).toBeUndefined();
    }
  });

  it("says how long a job has been going, and how long a finished one took", () => {
    const now = new Date("2026-08-27T10:06:30Z");

    const running = rows(
      [job({ id: "a", status: "RUNNING", startedAt: "2026-08-27T10:00:00Z" })],
      [],
      { now },
    );
    expect(running[0].took).toEqual({
      text: "running for {{ minutes }} min",
      params: { minutes: 7 },
    });

    const done = rows(
      [
        job({
          id: "b",
          status: "SUCCEEDED",
          startedAt: "2026-08-27T10:00:00Z",
          finishedAt: "2026-08-27T10:00:12Z",
        }),
      ],
      [],
      { now },
    );
    expect(done[0].took).toEqual({
      text: "took {{ seconds }} s",
      params: { seconds: 12 },
    });
  });

  it("measures a finished job between its own timestamps, not against now", () => {
    const long = new Date("2027-01-01T00:00:00Z");
    const [row] = rows(
      [
        job({
          status: "SUCCEEDED",
          startedAt: "2026-08-27T10:00:00Z",
          finishedAt: "2026-08-27T11:30:00Z",
        }),
      ],
      [],
      { now: long },
    );

    expect(row.took).toEqual({
      text: "took {{ hours }} h {{ minutes }} min",
      params: { hours: 1, minutes: 30 },
    });
  });

  it("says nothing about a job that never started, or one with nonsense times", () => {
    const now = new Date("2026-08-27T10:00:00Z");

    expect(duration(job({ status: "QUEUED" }), now)).toBeUndefined();
    expect(duration(job({ startedAt: "the other day" }), now)).toBeUndefined();
    expect(
      duration(
        job({
          startedAt: "2026-08-27T10:00:00Z",
          finishedAt: "2026-08-27T09:00:00Z",
        }),
        now,
      ),
    ).toBeUndefined();
  });

  it("keeps asking while something runs, and stops when nothing does", () => {
    expect(stillRunning(rows([job({ status: "RUNNING" })], []))).toBe(true);
    expect(
      stillRunning(
        rows([job({ status: "SUCCEEDED" })], [job({ status: "FAILED" })]),
      ),
    ).toBe(false);
  });

  it("offers the engine output where there is any, and nothing where there is none", () => {
    const [withLog, withError, bare] = rows(
      [
        job({ id: "a", progress: ["Exported 9 managed objects"] }),
        job({
          id: "b",
          status: "FAILED",
          error: "tenant t22222 is not subscribed",
        }),
        job({ id: "c", status: "QUEUED" }),
      ],
      [],
    );

    expect(hasDetail(withLog)).toBe(true);
    expect(hasDetail(withError)).toBe(true);
    expect(hasDetail(bare)).toBe(false);
  });

  it("gives each outcome its own label, so five statuses do not read as one", () => {
    const classes = rows(
      [
        job({ id: "a", status: "RUNNING" }),
        job({ id: "b", status: "SUCCEEDED" }),
        job({ id: "c", status: "FAILED" }),
        job({ id: "d", status: "INTERRUPTED" }),
        job({ id: "e", status: "QUEUED" }),
      ],
      [],
    ).map(labelClass);

    expect(classes).toEqual([
      "label-info",
      "label-success",
      "label-danger",
      "label-warning",
      "label-default",
    ]);
    expect(new Set(classes).size).toBe(5);
  });

  it("says which failures the reader can do something about", () => {
    expect(
      explain(
        new ApiError("forbidden", 403, "you need ROLE_DEVICE_MIGRATION_ADMIN"),
      ).title,
    ).toBe("Not allowed here");
    expect(
      explain(new ApiError("busy", 429, "a migration is already running"))
        .title,
    ).toBe("The service is busy");
    expect(explain(new Error("offline")).detail).toContain("offline");
  });
});

describe("how often the list reads itself again", () => {
  it("is slow while nothing is happening", () => {
    expect(refreshInterval(rows([job({ status: "SUCCEEDED" })], []))).toBe(
      IDLE_REFRESH_MS,
    );
    expect(refreshInterval([])).toBe(IDLE_REFRESH_MS);
  });

  it("is quicker while something is running", () => {
    expect(refreshInterval(rows([job({ status: "RUNNING" })], []))).toBe(
      RUNNING_REFRESH_MS,
    );
    expect(refreshInterval(rows([job({ status: "QUEUED" })], []))).toBe(
      RUNNING_REFRESH_MS,
    );
  });

  // It never stops: a migration started from another session, or from the
  // command line, has to turn up here too.
  it("keeps reading even when there is nothing to watch", () => {
    expect(
      refreshInterval(rows([job({ status: "FAILED" })], [])),
    ).toBeGreaterThan(0);
  });

  it("asks three times less often when idle than when busy", () => {
    expect(IDLE_REFRESH_MS).toBe(3 * RUNNING_REFRESH_MS);
  });
});

describe("a migration that has already been undone", () => {
  it("is not offered for undoing again", () => {
    const migration = job({ id: "m1", runId: "r1", status: "SUCCEEDED" });
    const undo = job({
      id: "u1",
      kind: "REVERT",
      status: "SUCCEEDED",
      revert: { runId: "r1", targetTenant: "t-target" },
    });

    const row = rows([migration], [undo]).find(
      (candidate) => candidate.job.id === "m1",
    )!;

    expect(row.job.id).toBe("m1");
    expect(row.revertable).toBe(false);
    expect(row.undone).toBe(true);
  });

  // A revert that failed or was cut short may have deleted some of what it found
  // and not the rest. Trying again is the useful thing to do with that, and it is
  // safe: a revert deletes what carries the marker, so a second run finds
  // whatever the first one left.
  it("is still offered when the undo did not finish", () => {
    for (const status of [
      "FAILED",
      "CANCELLED",
      "INTERRUPTED",
      "RUNNING",
    ] as const) {
      const row = rows(
        [job({ id: "m1", runId: "r1", status: "SUCCEEDED" })],
        [job({ id: "u1", kind: "REVERT", status, revert: { runId: "r1" } })],
      ).find((candidate) => candidate.job.id === "m1")!;

      expect(row.revertable).toBe(true);
      expect(row.undone).toBe(false);
    }
  });

  it("is unaffected by an undo of a different run", () => {
    const row = rows(
      [job({ id: "m1", runId: "r1", status: "SUCCEEDED" })],
      [
        job({
          id: "u1",
          kind: "REVERT",
          status: "SUCCEEDED",
          revert: { runId: "r-other" },
        }),
      ],
    ).find((candidate) => candidate.job.id === "m1")!;

    expect(row.revertable).toBe(true);
  });
});

describe("what the list says about tenants and objects", () => {
  const tenants = byId([
    { id: "t-a", name: "Werk Berlin", domain: "t-a.example.com" },
    { id: "t-b", domain: "t-b.example.com" },
  ]);

  it("names the source and target in columns of their own", () => {
    const [row] = rows(
      [
        job({
          request: {
            sourceTenant: "t-a",
            targetTenant: "t-b",
            rootId: "1399229",
          },
          rootName: "Plant Berlin",
        }),
      ],
      [],
      { tenants },
    );

    expect(row.source).toBe("Werk Berlin (t-a)");
    expect(row.target).toBe("t-b.example.com (t-b)");
  });

  it("says what was migrated by name as well as by id", () => {
    const [row] = rows(
      [
        job({
          request: {
            sourceTenant: "t-a",
            targetTenant: "t-b",
            rootId: "1399229",
          },
          rootName: "Plant Berlin",
        }),
      ],
      [],
      { tenants },
    );

    expect(row.what).toEqual({
      text: "{{ name }} ({{ id }})",
      params: { name: "Plant Berlin", id: "1399229" },
    });
  });

  // Records written before the name was recorded have only the id, and that is
  // what they show.
  it("shows the id alone when no name was recorded", () => {
    const [row] = rows(
      [
        job({
          request: {
            sourceTenant: "t-a",
            targetTenant: "t-b",
            rootId: "1399229",
          },
        }),
      ],
      [],
      { tenants },
    );

    expect(row.what).toEqual({ text: "{{ id }}", params: { id: "1399229" } });
  });

  it("describes an undo by the run it undoes, in the tenant it deletes from", () => {
    const [row] = rows(
      [],
      [job({ kind: "REVERT", revert: { runId: "r1", targetTenant: "t-b" } })],
      { tenants },
    );

    expect(row.what).toEqual({
      text: "Undo of {{ runId }}",
      params: { runId: "r1" },
    });
    expect(row.source).toBe("");
    expect(row.target).toBe("t-b.example.com (t-b)");
  });
});
