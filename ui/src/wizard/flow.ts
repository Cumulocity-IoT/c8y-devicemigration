import {
  ApiError,
  type EstimateResponse,
  type InventoryObject,
  type Job,
  type MigrationApi,
  type MigrationRequest
} from '../api/client';
import { availability, canStart, next, previous, type StepAvailability, type StepId } from './steps';

/**
 * The wizard, without Angular.
 *
 * A plain class taking the API in its constructor, so every rule that decides
 * what happens next - when a pre-flight is worth doing, what invalidates an
 * estimate, which tenants may be offered as a target - is testable with a stub
 * instead of a browser. The component around it renders this and nothing else.
 */

export interface Tenant {
  id: string;
  /** Why it cannot be chosen, empty when it can. */
  unavailable: string;
}

export interface Failure {
  title: string;
  detail: string;
  /** Whether the same thing is worth trying again. */
  retryable: boolean;
}

/** What the wizard is asking for. Mirrors the API's own request. */
export interface Choices {
  sourceTenant?: string;
  root?: InventoryObject;
  targetTenant?: string;
  childTypes: string;
  noChildren: boolean;
  dataTypes: string;
  dateFrom: string;
  dateTo: string;
}

export const DEFAULT_CHOICES: Choices = {
  childTypes: 'asset,device',
  noChildren: false,
  dataTypes: '',
  dateFrom: '',
  dateTo: ''
};

export class WizardFlow {
  step: StepId = 'introduction';
  choices: Choices = { ...DEFAULT_CHOICES };

  ownerTenant = '';
  tenants: string[] = [];

  /** Set once both service users have been proven. */
  preflightPassed = false;

  estimate?: EstimateResponse;
  job?: Job;

  failure?: Failure;
  busy = false;

  constructor(private readonly api: MigrationApi) {}

  // --- what the screen asks ---------------------------------------------------

  get state() {
    return {
      sourceTenant: this.choices.sourceTenant,
      rootId: this.choices.root?.id,
      targetTenant: this.choices.targetTenant,
      preflightPassed: this.preflightPassed,
      estimated: !!this.estimate,
      withinLimits: this.estimate?.withinLimits,
      jobId: this.job?.id
    };
  }

  can(step: StepId): StepAvailability {
    return availability(step, this.state);
  }

  get startable(): StepAvailability {
    return canStart(this.state);
  }

  /**
   * The tenants offerable as a source.
   *
   * The owner tenant is listed but not selectable: it is where job records live,
   * and migrating out of it is a different operation from the one this wizard
   * describes. Saying so beats hiding it and leaving somebody looking.
   */
  get sourceTenants(): Tenant[] {
    return this.tenants.map(id => ({
      id,
      unavailable: id === this.ownerTenant ? 'this tenant, where migrations are recorded' : ''
    }));
  }

  /** The tenants offerable as a target: anything but the source. */
  get targetTenants(): Tenant[] {
    return this.tenants.map(id => ({
      id,
      unavailable: id === this.choices.sourceTenant ? 'already the source' : ''
    }));
  }

  // --- moving ----------------------------------------------------------------

  async open(step: StepId): Promise<void> {
    if (!this.can(step).reachable) {
      return;
    }
    this.step = step;
    this.failure = undefined;

    // Each step earns its own answer when it opens, rather than on a button:
    // arriving at the target step is what makes a pre-flight worth doing.
    if (step === 'target' && !this.preflightPassed) {
      return;
    }
    if (step === 'review' && !this.estimate) {
      await this.refreshEstimate();
    }
  }

  async forward(): Promise<void> {
    const after = next(this.step);
    if (after) {
      await this.open(after);
    }
  }

  back(): void {
    const before = previous(this.step);
    if (before) {
      this.step = before;
      this.failure = undefined;
    }
  }

  // --- choosing --------------------------------------------------------------

  /** Choosing a different source invalidates everything downstream of it. */
  chooseSource(tenant: string): void {
    if (this.choices.sourceTenant === tenant) {
      return;
    }
    this.choices.sourceTenant = tenant;
    this.choices.root = undefined;
    this.invalidateTarget();
  }

  chooseRoot(root: InventoryObject | undefined): void {
    this.choices.root = root;
    this.estimate = undefined;
  }

  chooseTarget(tenant: string): void {
    if (this.choices.targetTenant === tenant) {
      return;
    }
    this.choices.targetTenant = tenant;
    this.preflightPassed = false;
    this.estimate = undefined;
  }

  /**
   * Anything that changes what would move invalidates the estimate. An estimate
   * describing a different migration from the one about to run is worse than no
   * estimate.
   */
  changeScope(change: Partial<Choices>): void {
    this.choices = { ...this.choices, ...change };
    this.estimate = undefined;
  }

  private invalidateTarget(): void {
    // A target that is now the source is no longer a target.
    if (this.choices.targetTenant === this.choices.sourceTenant) {
      this.choices.targetTenant = undefined;
    }
    this.preflightPassed = false;
    this.estimate = undefined;
  }

  // --- talking to the service ------------------------------------------------

  async loadTenants(): Promise<void> {
    await this.attempt(async () => {
      const answer = await this.api.tenants();
      this.ownerTenant = answer.ownerTenant ?? '';
      this.tenants = answer.tenants ?? [];
    });
  }

  async search(text: string): Promise<InventoryObject[]> {
    if (!this.choices.sourceTenant) {
      return [];
    }
    let found: InventoryObject[] = [];
    await this.attempt(async () => {
      const answer = await this.api.search(this.choices.sourceTenant!, text, !text);
      found = answer.objects ?? [];
    });
    return found;
  }

  async children(id: string): Promise<InventoryObject[]> {
    if (!this.choices.sourceTenant) {
      return [];
    }
    let found: InventoryObject[] = [];
    await this.attempt(async () => {
      const answer = await this.api.children(
        this.choices.sourceTenant!,
        id,
        this.choices.noChildren ? 'asset,device' : this.choices.childTypes
      );
      found = answer.objects ?? [];
    });
    return found;
  }

  async preflight(): Promise<void> {
    const { sourceTenant, targetTenant } = this.choices;
    if (!sourceTenant || !targetTenant) {
      return;
    }

    await this.attempt(async () => {
      await this.api.preflight({ sourceTenant, targetTenant });
      this.preflightPassed = true;
    });
  }

  async refreshEstimate(): Promise<void> {
    const { sourceTenant, root } = this.choices;
    if (!sourceTenant || !root?.id) {
      return;
    }

    await this.attempt(async () => {
      this.estimate = await this.api.estimate({
        sourceTenant,
        rootId: root.id!,
        childTypes: this.choices.noChildren ? undefined : this.choices.childTypes,
        noChildren: this.choices.noChildren || undefined,
        dataTypes: this.choices.dataTypes || undefined,
        dateFrom: this.choices.dateFrom || undefined,
        dateTo: this.choices.dateTo || undefined
      });
    });
  }

  async start(): Promise<void> {
    if (!this.startable.reachable) {
      return;
    }

    await this.attempt(async () => {
      this.job = await this.api.startMigration(this.request());
      this.step = 'progress';
    });
  }

  /** The migration as it will be asked for. */
  request(): MigrationRequest {
    const { sourceTenant, targetTenant, root } = this.choices;
    const request: MigrationRequest = {
      sourceTenant: sourceTenant!,
      targetTenant: targetTenant!,
      rootId: root!.id!
    };

    // Only what was chosen: an omitted field means the service's own default,
    // and sending an empty one would be refused.
    if (this.choices.noChildren) {
      request.noChildren = true;
    } else if (this.choices.childTypes !== DEFAULT_CHOICES.childTypes) {
      request.childTypes = this.choices.childTypes;
    }
    if (this.choices.dataTypes) {
      request.dataTypes = this.choices.dataTypes;
    }
    if (this.choices.dateFrom) {
      request.dateFrom = this.choices.dateFrom;
    }
    if (this.choices.dateTo) {
      request.dateTo = this.choices.dateTo;
    }
    return request;
  }

  /** Runs an API call, turning a refusal into something a screen can show. */
  private async attempt(work: () => Promise<void>): Promise<void> {
    this.busy = true;
    this.failure = undefined;
    try {
      await work();
    } catch (error) {
      this.failure = failureFrom(error);
    } finally {
      this.busy = false;
    }
  }
}

export function failureFrom(error: unknown): Failure {
  if (!(error instanceof ApiError)) {
    return { title: 'Something went wrong', detail: String(error), retryable: false };
  }

  const titles: Record<string, string> = {
    unauthenticated: 'Sign in again',
    forbidden: 'Not allowed here',
    busy: 'The service is busy',
    notFound: 'Not found',
    refused: 'That cannot be done',
    failed: 'The migration service could not answer'
  };

  return {
    title: titles[error.kind] ?? 'That cannot be done',
    detail: error.message,
    retryable: error.retryable
  };
}
