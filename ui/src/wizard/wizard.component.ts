import { CommonModule } from '@angular/common';
import { Component, OnInit, computed, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { CoreModule } from '@c8y/ngx-components';

import type { InventoryObject } from '../api/client';
import { MigrationService } from '../api/migration.service';
import { deviceProgress, isRunning, present } from '../progress';
import { WizardFlow } from './flow';
import { STEPS, type StepId } from './steps';

/** How often a running job is asked about. */
const POLL_MS = 3000;

/** A row in the device picker, and whether it has been expanded. */
interface TreeRow {
  object: InventoryObject;
  depth: number;
  expanded: boolean;
}

/**
 * The four steps and the progress screen.
 *
 * Holds no rules: WizardFlow decides what is reachable, what invalidates an
 * estimate and what a refusal means. This turns that into a screen, and owns the
 * two things a screen is actually responsible for - the device tree it is
 * showing, and the polling.
 */
@Component({
  selector: 'c8y-device-migration-wizard',
  standalone: true,
  imports: [CommonModule, CoreModule, FormsModule],
  templateUrl: './wizard.component.html'
})
export class WizardComponent implements OnInit {
  readonly steps = STEPS;
  readonly flow: WizardFlow;

  /** Bumped whenever the flow changes, so the template re-reads it. */
  readonly version = signal(0);

  readonly search = signal('');
  readonly rows = signal<TreeRow[]>([]);
  readonly searching = signal(false);

  readonly job = computed(() => {
    this.version();
    return this.flow.job;
  });
  readonly jobStatus = computed(() => {
    const job = this.job();
    return job ? present(job) : undefined;
  });
  readonly jobProgress = computed(() => deviceProgress(this.job()?.progress));

  private polling?: ReturnType<typeof setTimeout>;

  constructor(private readonly migration: MigrationService) {
    this.flow = new WizardFlow(migration.api);
  }

  async ngOnInit(): Promise<void> {
    await this.act(() => this.flow.loadTenants());
  }

  // --- navigation ------------------------------------------------------------

  async open(step: StepId): Promise<void> {
    await this.act(() => this.flow.open(step));
  }

  async forward(): Promise<void> {
    await this.act(async () => {
      await this.flow.forward();
      // Arriving at the target step is what makes proving both tenants worth
      // doing; it reads nothing from either hierarchy.
      if (this.flow.step === 'target' && !this.flow.preflightPassed) {
        await this.flow.preflight();
      }
    });
  }

  back(): void {
    this.flow.back();
    this.version.update(v => v + 1);
  }

  // --- choosing --------------------------------------------------------------

  async chooseSource(tenant: string): Promise<void> {
    this.flow.chooseSource(tenant);
    this.rows.set([]);
    this.search.set('');
    await this.find();
  }

  async chooseTarget(tenant: string): Promise<void> {
    this.flow.chooseTarget(tenant);
    await this.act(() => this.flow.preflight());
  }

  chooseRoot(object: InventoryObject): void {
    this.flow.chooseRoot(object);
    this.version.update(v => v + 1);
  }

  async changeScope(change: Parameters<WizardFlow['changeScope']>[0]): Promise<void> {
    this.flow.changeScope(change);
    this.version.update(v => v + 1);
  }

  // --- the device picker -----------------------------------------------------

  /** Searches, or lists the roots when the box is empty. */
  async find(): Promise<void> {
    this.searching.set(true);
    try {
      const found = await this.flow.search(this.search());
      this.rows.set(found.map(object => ({ object, depth: 0, expanded: false })));
    } finally {
      this.searching.set(false);
      this.version.update(v => v + 1);
    }
  }

  /** Expands or collapses one row, keeping the tree flat underneath. */
  async toggle(row: TreeRow): Promise<void> {
    const rows = [...this.rows()];
    const at = rows.indexOf(row);
    if (at < 0) {
      return;
    }

    if (row.expanded) {
      // Everything deeper than this row, up to the next row at its own depth.
      let end = at + 1;
      while (end < rows.length && rows[end].depth > row.depth) {
        end++;
      }
      rows.splice(at + 1, end - at - 1);
      rows[at] = { ...row, expanded: false };
      this.rows.set(rows);
      return;
    }

    const children = await this.flow.children(row.object.id!);
    rows[at] = { ...row, expanded: true };
    rows.splice(
      at + 1,
      0,
      ...children.map(object => ({ object, depth: row.depth + 1, expanded: false }))
    );
    this.rows.set(rows);
    this.version.update(v => v + 1);
  }

  /** Whether a row can be expanded at all. */
  hasChildren(object: InventoryObject): boolean {
    return Object.values(object.children ?? {}).some(count => count > 0);
  }

  childSummary(object: InventoryObject): string {
    const counts = Object.entries(object.children ?? {}).filter(([, count]) => count > 0);
    if (!counts.length) {
      return '';
    }
    return counts.map(([kind, count]) => `${count} ${kind}`).join(', ');
  }

  // --- running ---------------------------------------------------------------

  async start(): Promise<void> {
    await this.act(() => this.flow.start());
    this.poll();
  }

  async cancel(): Promise<void> {
    const job = this.flow.job;
    if (!job?.id) {
      return;
    }
    await this.act(() => this.migration.api.cancelMigration(job.id!));
  }

  /**
   * Asks after the running job until it stops.
   *
   * Every call costs the service a round trip to the platform to verify the
   * caller, so this is deliberately unhurried, and it stops the moment the job
   * is finished rather than polling a settled record forever.
   */
  private poll(): void {
    clearTimeout(this.polling);

    const job = this.flow.job;
    if (!job?.id || !isRunning(job)) {
      return;
    }

    this.polling = setTimeout(async () => {
      try {
        this.flow.job = await this.migration.api.migration(job.id!);
      } catch {
        // A poll that fails is not the job failing; the next one may work, and
        // the record is the truth either way.
      }
      this.version.update(v => v + 1);
      this.poll();
    }, POLL_MS);
  }

  /** Runs a step and lets the template know something changed. */
  private async act(work: () => Promise<void>): Promise<void> {
    await work();
    this.version.update(v => v + 1);
  }
}
