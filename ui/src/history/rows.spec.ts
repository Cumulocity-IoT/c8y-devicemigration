import { ApiError, type Job } from "../api/client";
import {
  actionsFor,
  deleteWarnings,
  duration,
  explain,
  hasDetail,
  IDLE_REFRESH_MS,
  keepingIdentity,
  liveRemoteTargets,
  outcome,
  scopeOf,
  statusTag,
  refreshInterval,
  serviceAbsent,
  revertWarnings,
  revertable,
  rows,
  RUNNING_REFRESH_MS,
  stillRunning,
  undosDeleted,
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

  it("measures a running job against now, and a finished one against its end", () => {
    const now = new Date("2026-08-27T10:06:30Z");

    const running = rows(
      [job({ id: "a", status: "RUNNING", startedAt: "2026-08-27T10:00:00Z" })],
      [],
      { now },
    );
    expect(running[0].took).toEqual({
      text: "{{ minutes }} min",
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
      text: "{{ seconds }} s",
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
      text: "{{ hours }} h {{ minutes }} min",
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

  it("gives each outcome the platform's tag for its tone", () => {
    const tags = rows(
      [
        job({ id: "a", status: "RUNNING" }),
        job({ id: "b", status: "SUCCEEDED" }),
        job({ id: "c", status: "FAILED" }),
        job({ id: "d", status: "INTERRUPTED" }),
        job({ id: "e", status: "QUEUED" }),
      ],
      [],
    ).map(statusTag);

    expect(tags).toEqual([
      "tag tag--info",
      "tag tag--success",
      "tag tag--danger",
      "tag tag--warning",
      "tag tag--default",
    ]);
  });

  it("falls back to a neutral tag for a status it has never heard of", () => {
    // Cast because the generated types know today's statuses; a service that
    // adds one is exactly the case this branch exists for, and it must not
    // render as a blank cell.
    const unknown = job({ id: "a", status: "WAT" as Job["status"] });

    expect(statusTag(rows([unknown], [])[0])).toBe("tag tag--default");
  });

  describe("the one status a row shows", () => {
    const migration = { id: "m1", runId: "r1", status: "SUCCEEDED" } as const;
    const undoOf = (status: Job["status"]) =>
      job({ id: `u-${status}`, kind: "REVERT", status, revert: { runId: "r1" } });

    it("says Reverted instead of a success qualified by an undo", () => {
      const row = rows([job(migration)], [undoOf("SUCCEEDED")])[0];

      // Not "Succeeded — already undone": the migration's objects are gone, so
      // calling the row succeeded is the wrong single word for it.
      expect(row.shown.label).toBe("Reverted");
      expect(statusTag(row)).toBe("tag tag--danger");
    });

    it("says Reverting while an undo is going", () => {
      const row = rows([job(migration)], [undoOf("RUNNING")])[0];

      expect(row.shown.label).toBe("Reverting");
      expect(statusTag(row)).toBe("tag tag--info");
    });

    it("says an undo failed rather than leaving the row reading Succeeded", () => {
      const row = rows([job(migration)], [undoOf("FAILED")])[0];

      expect(row.shown.label).toBe("Revert failed");
      expect(statusTag(row)).toBe("tag tag--warning");
      // The half-deleted state is the thing somebody has to be told about.
      expect(row.shown.caveat).toContain("may already be gone");
    });

    it("prefers a later successful undo over an earlier failed one", () => {
      const row = rows(
        [job(migration)],
        [undoOf("FAILED"), undoOf("SUCCEEDED")],
      )[0];

      expect(row.shown.label).toBe("Reverted");
    });

    it("leaves a migration nobody undid with its own status", () => {
      expect(rows([job(migration)], [])[0].shown.label).toBe("Succeeded");
      expect(outcome(job(migration), []).label).toBe("Succeeded");
    });
  });

  describe("undos and their migration", () => {
    it("attaches an undo to the migration it undid instead of listing it", () => {
      const list = rows(
        [job({ id: "m1", runId: "r1", status: "SUCCEEDED" })],
        [
          job({
            id: "u1",
            kind: "REVERT",
            status: "SUCCEEDED",
            revert: { runId: "r1", targetTenant: "t-target" },
          }),
        ],
      );

      // One row, not two: the undo is read in the migration's expansion.
      expect(list).toHaveLength(1);
      expect(list[0].job.id).toBe("m1");
      expect(list[0].undos.map(u => u.job.id)).toEqual(["u1"]);
    });

    it("keeps an undo whose migration is not listed as a row of its own", () => {
      // A revert run from the command line against a run id whose migration has
      // aged out of the records. A job with no parent must not be nowhere.
      const list = rows(
        [],
        [job({ id: "u1", kind: "REVERT", status: "SUCCEEDED", revert: { runId: "gone" } })],
      );

      expect(list).toHaveLength(1);
      expect(list[0].job.id).toBe("u1");
      expect(list[0].undos).toEqual([]);
      expect(list[0].revertable).toBe(false);
    });

    it("orders several undos of one migration newest first", () => {
      const list = rows(
        [job({ id: "m1", runId: "r1", status: "SUCCEEDED" })],
        [
          job({ id: "older", kind: "REVERT", status: "FAILED", revert: { runId: "r1" }, queuedAt: "2026-08-01T10:00:00Z" }),
          job({ id: "newer", kind: "REVERT", status: "FAILED", revert: { runId: "r1" }, queuedAt: "2026-08-02T10:00:00Z" }),
        ],
      );

      expect(list[0].undos.map(u => u.job.id)).toEqual(["newer", "older"]);
    });
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

  // The ratio used to be the assertion, which only described the numbers that
  // happened to be there. What matters is that watching a job is quick enough to
  // look alive, and that idling is cheap - so both numbers are pinned, and
  // changing either is then a deliberate act rather than a silent drift.
  it("watches a running job every second and idles at thirty", () => {
    expect(RUNNING_REFRESH_MS).toBe(1_000);
    expect(IDLE_REFRESH_MS).toBe(30_000);
    expect(RUNNING_REFRESH_MS).toBeLessThan(IDLE_REFRESH_MS);
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
    expect(row.shown.label).toBe("Reverted");
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
      expect(row.shown.label).not.toBe("Reverted");
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

  describe("keeping row identity across a refresh", () => {
    // The grid remembers expanded rows in a Map keyed by the row object, so an
    // unchanged row has to come back as the *same* object or the refresh
    // collapses whatever the reader had open.
    const row = (id: string, status: string) =>
      ({ id, status, undos: [] }) as never;

    it("hands back the previous object when nothing changed", () => {
      const before = [row("a", "Succeeded"), row("b", "Running")];
      const after = keepingIdentity(before, [
        row("a", "Succeeded"),
        row("b", "Running"),
      ]);

      expect(after[0]).toBe(before[0]);
      expect(after[1]).toBe(before[1]);
    });

    it("uses the fresh object for a row whose content moved on", () => {
      const before = [row("a", "Running")];
      const fresh = [row("a", "Succeeded")];
      const after = keepingIdentity(before, fresh);

      // Reusing the old object here would show "Running" for ever.
      expect(after[0]).toBe(fresh[0]);
      expect(after[0]).not.toBe(before[0]);
    });

    it("passes through rows it has never seen", () => {
      const fresh = [row("new", "Queued")];

      expect(keepingIdentity([], fresh)[0]).toBe(fresh[0]);
    });
  });

  describe("what a migration included", () => {
    it("reads an omitted childTypes as the API's default, not as none", () => {
      const scope = scopeOf(job({ id: "m", request: { sourceTenant: "s", rootId: "1" } }));

      expect(scope.relationships).toEqual(["asset", "device"]);
      expect(scope.rootOnly).toBe(false);
      // Silence about data really does mean none, unlike silence about children.
      expect(scope.dataTypes).toEqual([]);
    });

    it("says the root moved alone when noChildren was set", () => {
      const scope = scopeOf(
        job({ id: "m", request: { sourceTenant: "s", rootId: "1", noChildren: true } }),
      );

      expect(scope.rootOnly).toBe(true);
      expect(scope.relationships).toEqual([]);
    });

    // The download action is offered only while the file is still collectable,
    // so before this the record was the only place a completed provisioning
    // survived - and nothing read it.
    it("carries how the devices got credentials, from the record", () => {
      const provisioned = scopeOf(
        job({
          id: "m",
          request: { sourceTenant: "s", rootId: "1", provisionCredentials: true },
          credentialsIssued: 42,
        }),
      );

      expect(provisioned.credentialsIssued).toBe(42);
      expect(provisioned.selfService).toBe(false);
    });

    it("notices a migration whose devices collect their own", () => {
      const self = scopeOf(
        job({
          id: "m",
          request: { sourceTenant: "s", rootId: "1", deviceSelfCredentials: true },
        }),
      );

      expect(self.selfService).toBe(true);
      // Nothing was issued at migration time - each device mints its own later.
      expect(self.credentialsIssued).toBe(0);
    });

    it("carries a certificate fleet, which issues nothing but still stamps", () => {
      const scope = scopeOf(
        job({
          id: "m",
          request: { sourceTenant: "s", rootId: "1", certificateFleet: true },
        }),
      );

      expect(scope.certificateFleet).toBe(true);
      expect(scope.credentialsIssued).toBe(0);
      expect(scope.selfService).toBe(false);
    });

    it("reads a migration that provisioned nothing as none", () => {
      const scope = scopeOf(job({ id: "m", request: { sourceTenant: "s", rootId: "1" } }));

      expect(scope.credentialsIssued).toBe(0);
      expect(scope.selfService).toBe(false);
    });

    it("splits what was asked for, and notices a remote target", () => {
      const scope = scopeOf(
        job({
          id: "m",
          request: {
            sourceTenant: "s",
            rootId: "1",
            childTypes: "asset,addition",
            dataTypes: "measurements,alarms",
            dateFrom: "-14d",
            remoteTargetId: "rt-1",
          },
        }),
      );

      expect(scope.relationships).toEqual(["asset", "addition"]);
      expect(scope.dataTypes).toEqual(["measurements", "alarms"]);
      expect(scope.from).toBe("-14d");
      expect(scope.remote).toBe(true);
    });
  });

  // The row action for the other half of a certificate-fleet move. It does not
  // go false once the anchor has moved, because a cutover leaves no record to
  // read - the dialogue answers that by listing the source's truststore live.
  describe("offering the trust-anchor move", () => {
    const rowFor = (fields: Partial<Job>) => rows([job(fields)], [])[0];

    it("offers it for a succeeded certificate fleet", () => {
      expect(
        rowFor({ request: { sourceTenant: "s", rootId: "1", certificateFleet: true } })
          .trustAnchorMovable,
      ).toBe(true);
    });

    it("does not offer it before the objects are there", () => {
      // Moving the anchor first would send the fleet to a tenant with nothing
      // in it, and every device would auto-register an empty twin.
      expect(
        rowFor({
          status: "RUNNING",
          request: { sourceTenant: "s", rootId: "1", certificateFleet: true },
        }).trustAnchorMovable,
      ).toBe(false);
      expect(
        rowFor({
          status: "FAILED",
          request: { sourceTenant: "s", rootId: "1", certificateFleet: true },
        }).trustAnchorMovable,
      ).toBe(false);
    });

    it("does not offer it for a migration of any other kind", () => {
      expect(rowFor({ request: { sourceTenant: "s", rootId: "1" } }).trustAnchorMovable).toBe(
        false,
      );
      expect(
        rowFor({
          request: { sourceTenant: "s", rootId: "1", provisionCredentials: true },
        }).trustAnchorMovable,
      ).toBe(false);
    });
  });

  describe("confirming an undo", () => {
    const migrated = (over: Record<string, unknown> = {}) =>
      rows(
        [job({ id: "m1", runId: "r1", status: "SUCCEEDED", ...over })],
        [],
      )[0];

    it("always warns that deleting a group takes more with it", () => {
      // Cumulocity propagates a group deletion to its subgroups, which can
      // remove objects this migration never created. True of every undo.
      expect(revertWarnings(migrated())[0]).toContain("propagates");
    });

    // Undoing a migration that issued credentials invalidates the file somebody
    // may be holding, and that is the point of undoing it rather than a side
    // effect - so the confirmation has to say so before the button is pressed.
    it("says that device credentials stop working", () => {
      const warnings = revertWarnings(migrated({ credentialsIssued: 3 }));

      expect(warnings.some(w => w.includes("stop working"))).toBe(true);
    });

    it("says nothing about credentials for a migration that issued none", () => {
      expect(revertWarnings(migrated()).some(w => w.includes("stop working"))).toBe(false);
      expect(
        revertWarnings(migrated({ credentialsIssued: 0 })).some(w => w.includes("stop working"))
      ).toBe(false);
    });

    // The one case where undoing can leave the target worse than it started:
    // once the anchor has moved the devices are arriving in that tenant on their
    // own, and deleting their twins only makes them register empty ones.
    it("warns that a certificate fleet cannot be called back by deleting its objects", () => {
      const warnings = revertWarnings(
        migrated({ request: { sourceTenant: "s", rootId: "1", certificateFleet: true } }),
      );

      expect(warnings.some(w => w.includes("will not send them back"))).toBe(true);
    });

    it("says nothing about certificates for a migration that is not one", () => {
      expect(
        revertWarnings(migrated()).some(w => w.includes("will not send them back")),
      ).toBe(false);
    });

    it("says when the run's own account of itself cannot be relied on", () => {
      const warnings = revertWarnings(migrated({ countsVerified: false }));

      expect(warnings.some(w => w.includes("cut short"))).toBe(true);
      expect(revertWarnings(migrated()).some(w => w.includes("cut short"))).toBe(false);
    });

    it("says when an earlier undo left the job half done", () => {
      const row = rows(
        [job({ id: "m1", runId: "r1", status: "SUCCEEDED" })],
        [job({ id: "u1", kind: "REVERT", status: "FAILED", revert: { runId: "r1" } })],
      )[0];

      expect(revertWarnings(row).some(w => w.includes("may already be gone"))).toBe(true);
    });
  });

  it("does not offer to undo a migration the service would refuse", () => {
    // A remote target was reached with a certificate the service held in memory
    // and no longer has, so it refuses the undo. A button whose only outcome is
    // that refusal is worse than no button.
    const row = rows(
      [
        job({
          id: "m1",
          runId: "r1",
          status: "SUCCEEDED",
          request: { sourceTenant: "s", rootId: "1", remoteTargetId: "rt-1" },
        }),
      ],
      [],
    )[0];

    expect(row.revertable).toBe(false);
  });

  describe("undoing a migration to a remote tenant", () => {
    const remote = (id: string) =>
      job({
        id: "m1",
        runId: "r1",
        status: "SUCCEEDED",
        request: { sourceTenant: "s", rootId: "1", remoteTargetId: id },
      });
    const now = new Date("2026-09-01T12:00:00Z");
    const target = (id: string, expiresAt: string) => ({ id, expiresAt });

    it("is offered while the certificate is still good", () => {
      // The whole point: the undo presents the same certificate the migration
      // did, so it works for exactly as long as that certificate does.
      const row = rows([remote("rt-1")], [], {
        now,
        remoteTargets: [target("rt-1", "2026-09-01T17:00:00Z")],
      })[0];

      expect(row.revertable).toBe(true);
      expect(row.revertExpired).toBe(false);
    });

    it("is withdrawn once the certificate has expired, and says which it is", () => {
      const row = rows([remote("rt-1")], [], {
        now,
        remoteTargets: [target("rt-1", "2026-09-01T11:59:00Z")],
      })[0];

      expect(row.revertable).toBe(false);
      // Not merely un-undoable: it *was* undoable, and a deadline passed.
      expect(row.revertExpired).toBe(true);
    });

    it("treats a certificate the service no longer holds as expired", () => {
      // What a restart leaves behind: the handle is on the record, the key is
      // not in memory any more, so the listing does not mention it at all.
      const row = rows([remote("rt-gone")], [], { now, remoteTargets: [] })[0];

      expect(row.revertable).toBe(false);
      expect(row.revertExpired).toBe(true);
    });

    it("does not blame a certificate for a run that was never undoable", () => {
      const unmarked = job({
        id: "m2",
        runId: "r2",
        status: "SUCCEEDED",
        request: { sourceTenant: "s", rootId: "1", remoteTargetId: "rt-gone", noFlag: true },
      });
      const row = rows([unmarked], [], { now, remoteTargets: [] })[0];

      expect(row.revertable).toBe(false);
      expect(row.revertExpired).toBe(false);
    });

    it("leaves a subscribed target alone whatever the certificates say", () => {
      const row = rows(
        [job({ id: "m3", runId: "r3", status: "SUCCEEDED" })],
        [],
        { now, remoteTargets: [] },
      )[0];

      expect(row.revertable).toBe(true);
      expect(row.revertExpired).toBe(false);
    });
  });

  it("counts a certificate as live only until the moment it expires", () => {
    const now = new Date("2026-09-01T12:00:00Z");
    const live = liveRemoteTargets(
      [
        { id: "future", expiresAt: "2026-09-01T12:00:01Z" },
        { id: "past", expiresAt: "2026-09-01T11:59:59Z" },
        { id: "exactly-now", expiresAt: "2026-09-01T12:00:00Z" },
        { id: "no-expiry" },
      ],
      now,
    );

    expect(live.has("future")).toBe(true);
    expect(live.has("past")).toBe(false);
    // The service compares the same way: at the deadline it is over.
    expect(live.has("exactly-now")).toBe(false);
    expect(live.has("no-expiry")).toBe(false);
  });
});


describe("forgetting a run", () => {
  const migrated = (over: Record<string, unknown> = {}, undos: Job[] = []) =>
    rows([job({ id: "m1", runId: "r1", status: "SUCCEEDED", ...over })], undos)[0];

  it("offers to remove a finished migration from the list", () => {
    expect(migrated().deletable).toBe(true);
  });

  it("does not offer it while the migration is still running", () => {
    // Its record is what this would delete, and the job is still writing it.
    expect(migrated({ status: "RUNNING" }).deletable).toBe(false);
  });

  it("does not offer it while an undo of it is running", () => {
    const row = migrated({}, [
      job({ id: "u1", kind: "REVERT", status: "RUNNING", revert: { runId: "r1" } }),
    ]);

    expect(row.deletable).toBe(false);
  });

  it("warns that a migration nobody undid stays in the target tenant", () => {
    // The row is revertable, so the record is the only thing that could still
    // remove those objects from here.
    const warnings = deleteWarnings(migrated());

    expect(warnings.some((w) => w.includes("has not been undone"))).toBe(true);
  });

  it("says nothing of the sort about one that was already undone", () => {
    const row = migrated({}, [
      job({ id: "u1", kind: "REVERT", status: "SUCCEEDED", revert: { runId: "r1" } }),
    ]);

    expect(row.revertable).toBe(false);
    expect(deleteWarnings(row)).toEqual([]);
  });

  it("warns when the objects are stranded by an expired certificate", () => {
    const row = rows(
      [
        job({
          id: "m1",
          runId: "r1",
          status: "SUCCEEDED",
          request: { sourceTenant: "s", rootId: "1", remoteTargetId: "rt-1" },
        }),
      ],
      [],
      { now: new Date("2026-09-01T12:00:00Z"), remoteTargets: [] },
    )[0];

    expect(deleteWarnings(row).some((w) => w.includes("still in the target tenant"))).toBe(
      true,
    );
  });

  it("names the undos that would go with the migration", () => {
    // Somebody removing one row should not find out afterwards that two went.
    const one = migrated({}, [
      job({ id: "u1", kind: "REVERT", status: "FAILED", revert: { runId: "r1" } }),
    ]);
    const two = migrated({}, [
      job({ id: "u1", kind: "REVERT", status: "FAILED", revert: { runId: "r1" } }),
      job({ id: "u2", kind: "REVERT", status: "SUCCEEDED", revert: { runId: "r1" } }),
    ]);

    expect(undosDeleted(migrated())).toBeUndefined();
    expect(undosDeleted(one)?.text).toContain("The undo of this migration");
    expect(undosDeleted(two)?.params).toEqual({ count: 2 });
  });
});


describe("what a row offers", () => {
  // Every row wears the same menu, so what varies is only what is in it. These
  // are the contents; row-actions.component.ts is the menu.
  const of = (migrations: Job[], reverts: Job[] = []) =>
    actionsFor(rows(migrations, reverts)[0]);

  it("offers undoing, reading and forgetting a finished migration", () => {
    expect(of([job({ id: "m1", progress: ["Device 1 of 1"] })])).toEqual([
      "revert",
      "log",
      "delete",
    ]);
  });

  it("offers stopping a running one, and not forgetting it", () => {
    // Its record is what a delete would remove, and the job is still writing it.
    expect(of([job({ id: "m1", status: "RUNNING", progress: ["Device 1 of 4"] })])).toEqual([
      "cancel",
      "log",
    ]);
  });

  it("does not offer to undo one that has been undone", () => {
    const actions = of(
      [job({ id: "m1", runId: "r1" })],
      [job({ id: "u1", kind: "REVERT", status: "SUCCEEDED", revert: { runId: "r1" } })],
    );

    expect(actions).toEqual(["delete"]);
  });

  it("explains an undo that a lapsed certificate has made impossible", () => {
    const actions = of([
      job({
        id: "m1",
        runId: "r1",
        request: { sourceTenant: "s", rootId: "1", remoteTargetId: "rt-1" },
      }),
    ]);

    // Not "revert": the service would refuse it. The entry says why instead.
    expect(actions).toContain("revertExpired");
    expect(actions).not.toContain("revert");
  });

  it("puts forgetting the record last, furthest from undoing it", () => {
    // They destroy opposite things, and the click that hits the wrong one is the
    // one that costs the most.
    const actions = of([job({ id: "m1", progress: ["Device 1 of 1"] })]);

    expect(actions[actions.length - 1]).toBe("delete");
    expect(actions.indexOf("revert")).toBeLessThan(actions.indexOf("delete"));
  });

  it("leaves out the log for a run that printed nothing", () => {
    expect(of([job({ id: "m1" })])).not.toContain("log");
  });
});
