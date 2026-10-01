import {
  ApiError,
  type EstimateResponse,
  type InventoryObject,
  type Job,
  type MigrationApi,
  type MigrationRequest,
  type PreflightConflict,
} from "../api/client";
import { gettext, message, type Message } from "../i18n";
import { tenantLabel, type TenantDescription } from "../tenants";
import { readiness, type Readiness } from "./steps";
import { windowProblem } from "./window";

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
  /** What to show: the name or host with the id, or the id alone. */
  label: string;
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

  /** Set instead of a subscribed targetTenant when the target is remote. */
  remoteTargetId?: string;

  childTypes: string;
  noChildren: boolean;
  dataTypes: string;
  dateFrom: string;
  dateTo: string;

  /**
   * How the migrated devices will authenticate in the target.
   *
   * One question with one answer, because the three ways of answering it are
   * mutually exclusive and the service refuses any pair of them. `none` is the
   * default: every other answer creates something - users, or an ownership stamp
   * that decides who may write a twin - and none of that should happen by
   * accident.
   */
  credentials: CredentialChoice;
}

/**
 * The four answers to "how will these devices authenticate".
 *
 *   - `none`        nothing is created; the copy cannot connect as itself
 *   - `file`        a device user each, downloaded once     (provisionCredentials)
 *   - `self`        each device collects its own            (deviceSelfCredentials)
 *   - `certificate` they keep the chain they have           (certificateFleet)
 */
export type CredentialChoice = 'none' | 'file' | 'self' | 'certificate';

export const DEFAULT_CHOICES: Choices = {
  childTypes: "asset,device",
  noChildren: false,
  dataTypes: "",
  dateFrom: "",
  dateTo: "",
  credentials: "none",
};

export class MigrationFlow {
  choices: Choices = { ...DEFAULT_CHOICES };

  ownerTenant = "";
  tenants: TenantDescription[] = [];

  /** Set once both service users have been proven. */
  preflightPassed = false;

  /**
   * The devices the target already has under the same external identity.
   *
   * Only ever filled when the credentials answer is `file`, because that is the
   * only one the service checks: it is the one that registers every device up
   * front, and registering an identity a tenant already holds replaces the
   * credential of whatever device is already using it. Empty is the ordinary
   * state and means nothing was found, not that nothing was asked.
   */
  credentialConflicts: PreflightConflict[] = [];

  /**
   * The remote target just established, if any, so the screen can show the CA to
   * register. The certificate here is public - the private half never leaves the
   * service.
   */
  remoteTarget?: import('../api/client').RemoteTargetEstablished;

  estimate?: EstimateResponse;
  job?: Job;

  failure?: Failure;

  /**
   * What is wrong with the start date, when something is.
   *
   * Kept apart from `failure` on purpose: this one belongs under the field that
   * caused it, where the person typing is already looking, rather than in the
   * alert at the top of the dialogue.
   */
  windowError?: Message;

  busy = false;

  constructor(private readonly api: MigrationApi) {}

  // --- what the screen asks ---------------------------------------------------

  /**
   * Everything still missing, and whether that is nothing.
   *
   * One screen shows every choice at once, so the useful answer is a list of
   * what is outstanding rather than which step may be opened.
   */
  get readiness(): Readiness {
    return readiness({
      sourceTenant: this.choices.sourceTenant,
      rootId: this.choices.root?.id,
      targetTenant: this.choices.targetTenant,
      preflightPassed: this.preflightPassed,
      estimated: !!this.estimate,
      withinLimits: this.estimate?.withinLimits,
      windowProblem: this.windowError?.text,
      credentialsRefused: this.credentialsRefused,
    });
  }

  /**
   * Whether the chosen credential option is refused for this target.
   *
   * Derived rather than stored, so it cannot outlive the conflicts it is about:
   * every path that changes the answer clears them, and a stale `true` here
   * would block a migration for a reason nothing on screen explains.
   */
  get credentialsRefused(): boolean {
    return this.credentialConflicts.length > 0;
  }

  /**
   * The tenants offerable as a source.
   *
   * The owner tenant is listed but not selectable: it is where job records live,
   * and migrating out of it is a different operation from the one this screen
   * describes. Saying so beats hiding it and leaving somebody looking.
   */
  get sourceTenants(): Tenant[] {
    return this.tenants.map((tenant) => ({
      id: tenant.id ?? "",
      label: tenantLabel(tenant),
      unavailable:
        tenant.id === this.ownerTenant
          ? gettext("this tenant, where migrations are recorded")
          : "",
    }));
  }

  /** The tenants offerable as a target: anything but the source. */
  get targetTenants(): Tenant[] {
    return this.tenants.map((tenant) => ({
      id: tenant.id ?? "",
      label: tenantLabel(tenant),
      unavailable:
        tenant.id === this.choices.sourceTenant
          ? gettext("already the source")
          : "",
    }));
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

  async chooseTarget(tenant: string): Promise<void> {
    if (this.choices.targetTenant === tenant && !this.choices.remoteTargetId) {
      return;
    }
    this.choices.targetTenant = tenant;
    this.choices.remoteTargetId = undefined;
    this.remoteTarget = undefined;
    this.preflightPassed = false;
    this.estimate = undefined;

    // On one screen there is no step to arrive at, so choosing is what makes
    // proving the tenants worth doing.
    await this.preflight();
  }

  /**
   * Establishes a certificate connection to a target this instance is not
   * subscribed to. Does not prove it yet: the operator has to register the
   * returned CA on that target first, then call checkRemoteTarget.
   */
  async establishRemoteTarget(host: string, tenant: string): Promise<void> {
    this.choices.remoteTargetId = undefined;
    this.remoteTarget = undefined;
    this.preflightPassed = false;
    this.estimate = undefined;

    await this.attempt(async () => {
      const established = await this.api.establishRemoteTarget({ host, tenant });
      this.remoteTarget = established;
      this.choices.remoteTargetId = established.id;
      this.choices.targetTenant = established.tenant;
      // A remote target is reached with a device access token, whose roles are
      // the device ones - it cannot register a device user, which is what both
      // credential answers need. The service refuses either, so clearing here
      // means the screen never offers something that would be rejected on
      // submit. A certificate fleet survives: it creates no user, only an owner
      // on objects that same token already owns.
      if (this.choices.credentials !== "certificate") {
        this.choices.credentials = "none";
        // The answer those conflicts were about is no longer chosen, and
        // nothing downstream of here would ask again before Start.
        this.forgetCredentialConflicts();
      }
    });
  }

  /** Proves a remote target once its CA has been registered. */
  async checkRemoteTarget(): Promise<void> {
    if (!this.choices.remoteTargetId) {
      return;
    }
    await this.preflight();
  }

  /**
   * Turns credential provisioning on or off.
   *
   * Deliberately not changeScope: that discards the estimate, because anything
   * changing what would *move* makes the estimate describe a different
   * migration. This changes what else gets created alongside, and the estimate
   * counts the registrable devices whether the box is ticked or not - so
   * invalidating it here would cost a round trip to learn a number already in
   * hand.
   */
  chooseCredentials(choice: CredentialChoice): void {
    this.choices = { ...this.choices, credentials: choice };
    if (choice !== "file") {
      this.forgetCredentialConflicts();
    }
  }

  /**
   * Drops a refusal that is no longer about anything.
   *
   * The one rule, in one place: conflicts belong to the answer that provisions a
   * device user per device, so they are cleared exactly when that answer stops
   * being the chosen one - and never merely because something they depend on
   * changed, which is what ensureCredentialCheck re-asks about instead.
   */
  private forgetCredentialConflicts(): void {
    this.credentialConflicts = [];
    this.credentialCheckedFor = undefined;
  }

  /**
   * Whether a credential can be issued into the target now chosen.
   *
   * False for a remote target, and for one reason that covers both answers that
   * issue one: such a target is reached with a device access token, whose
   * effective roles are the device ones. Bulk registration needs
   * ROLE_DEVICE_CONTROL_ADMIN and the token does not hold it, so neither a file
   * of passwords nor a device collecting its own is possible there.
   *
   * A certificate fleet is unaffected and stays on offer - see
   * `chooseCredentials`.
   */
  get canIssueCredentials(): boolean {
    return !this.choices.remoteTargetId;
  }

  /** The relationship kinds a migration can follow, in the API's own order. */
  static readonly CHILD_TYPES = ["asset", "device", "addition"] as const;

  /** The record kinds a migration can carry. */
  static readonly DATA_TYPES = ["measurements", "events", "alarms"] as const;

  /** Whether a kind is currently selected. */
  selected(field: "childTypes" | "dataTypes", kind: string): boolean {
    return split(this.choices[field]).includes(kind);
  }

  /**
   * Adds or removes one kind.
   *
   * The API takes a comma separated list, so the screen offers checkboxes and
   * the list is assembled here - in the order the API documents, because a
   * request that reads back differently from what was ticked is hard to trust.
   */
  toggle(field: "childTypes" | "dataTypes", kind: string): void {
    const order: readonly string[] =
      field === "childTypes"
        ? MigrationFlow.CHILD_TYPES
        : MigrationFlow.DATA_TYPES;

    const chosen = new Set(split(this.choices[field]));
    if (chosen.has(kind)) {
      chosen.delete(kind);
    } else {
      chosen.add(kind);
    }

    this.changeScope({
      [field]: order.filter((k) => chosen.has(k)).join(","),
    } as Partial<Choices>);
  }

  /**
   * Anything that changes what would move invalidates the estimate. An estimate
   * describing a different migration from the one about to run is worse than no
   * estimate.
   */
  changeScope(change: Partial<Choices>): void {
    this.choices = { ...this.choices, ...change };
    this.estimate = undefined;

    if ("dateFrom" in change) {
      // Checked here rather than left to the service: a typo belongs under the
      // field that has it, immediately, and not as a refusal at the top of the
      // dialogue after a round trip.
      this.windowError = windowProblem(this.choices.dateFrom);
    }
  }

  private invalidateTarget(): void {
    // A target that is now the source is no longer a target.
    if (this.choices.targetTenant === this.choices.sourceTenant) {
      this.choices.targetTenant = undefined;
    }
    // A remote target is proven against a specific source; changing the source
    // means proving it again, but the connection itself stays established.
    this.preflightPassed = false;
    this.estimate = undefined;
  }

  // --- talking to the service ------------------------------------------------

  async loadTenants(): Promise<void> {
    await this.attempt(async () => {
      const answer = await this.api.tenants();
      this.ownerTenant = answer.ownerTenant ?? "";
      this.tenants = answer.tenants ?? [];
    });
  }

  async preflight(): Promise<void> {
    const { sourceTenant, targetTenant, remoteTargetId, root } = this.choices;
    if (!sourceTenant || (!targetTenant && !remoteTargetId)) {
      return;
    }

    // The credential check is asked for, not implied: it costs a walk of the
    // hierarchy and a lookup per device, so it is worth doing only once there is
    // a hierarchy and only for the answer that registers every device up front.
    // A remote target cannot provision credentials at all, and the service
    // refuses the pair rather than checking it.
    const checkCredentials =
      this.choices.credentials === "file" && !!root?.id && !remoteTargetId;

    await this.attempt(async () => {
      const answer = await this.api.preflight(
        remoteTargetId
          ? { sourceTenant, remoteTargetId }
          : checkCredentials
            ? {
                sourceTenant,
                targetTenant,
                rootId: root!.id!,
                provisionCredentials: true,
                // The scope as the migration will ask for it, through the same
                // function, because which relationships are followed decides
                // which devices the check is about.
                ...this.scopeOfRequest()
              }
            : { sourceTenant, targetTenant }
      );
      if (checkCredentials) {
        this.credentialConflicts = answer.conflicts ?? [];
        this.credentialCheckedFor = this.credentialCheckKey;
      }
      this.preflightPassed = true;
    });
  }

  /**
   * The choices the credential check depends on, as one value to compare.
   *
   * Which devices would be registered depends on the hierarchy and on which
   * relationships are followed, not only on the two tenants - so narrowing the
   * child types is as much a reason to ask again as changing the target is.
   */
  private get credentialCheckKey(): string {
    const { sourceTenant, root, targetTenant, childTypes, noChildren, credentials } =
      this.choices;
    return [sourceTenant, root?.id, targetTenant, childTypes, noChildren, credentials].join("|");
  }

  private credentialCheckedFor?: string;

  /**
   * Re-checks the credential answer against the target when it is owed.
   *
   * Called wherever ensureEstimate is, and for the same reason: on one screen
   * every choice can invalidate the one before it, and a check that only ran on
   * the path somebody happened to take would pass by accident.
   *
   * Conflicts already found are deliberately *not* cleared first. A stale answer
   * that blocks is recoverable - this call replaces it a moment later - while a
   * stale answer that allows would let somebody start a migration the service
   * then refuses, which is the failure this whole check exists to prevent.
   */
  async ensureCredentialCheck(): Promise<void> {
    if (this.busy) {
      return;
    }
    const { sourceTenant, root, targetTenant, remoteTargetId } = this.choices;

    if (this.choices.credentials !== "file" || remoteTargetId) {
      // Nothing to check, and nothing that was found still applies.
      this.forgetCredentialConflicts();
      return;
    }
    if (!sourceTenant || !root?.id || !targetTenant) {
      return;
    }
    if (this.credentialCheckedFor === this.credentialCheckKey) {
      return;
    }
    await this.preflight();
  }

  async refreshEstimate(): Promise<void> {
    const { sourceTenant, root } = this.choices;
    if (!sourceTenant || !root?.id) {
      return;
    }
    if (this.windowError) {
      // Nothing to ask: the service would refuse the same value, and the field
      // already says why.
      return;
    }

    await this.attempt(async () => {
      this.estimate = await this.api.estimate({
        sourceTenant,
        rootId: root.id!,
        childTypes: this.choices.noChildren
          ? undefined
          : this.choices.childTypes,
        noChildren: this.choices.noChildren || undefined,
        dataTypes: this.choices.dataTypes || undefined,
        dateFrom: this.choices.dateFrom || undefined,
        dateTo: this.choices.dateTo || undefined,
      });
    });

    // The service is the authority on this syntax, and it may refuse a value the
    // check above accepted. Its words belong on the field too, not in the alert
    // at the top - which is where they used to appear, as "That cannot be done".
    if (this.failure && this.choices.dateFrom && isAboutTheDate(this.failure)) {
      // The service's own sentence, not one of ours: it goes through the
      // translation pipe like everything else and falls back to itself.
      this.windowError = { text: this.failure.detail };
      this.failure = undefined;
    }
  }

  /**
   * Estimates when there is something to estimate and no estimate yet.
   *
   * Called after every choice, so the summary keeps up with the screen. It does
   * nothing when an estimate already describes the current choices, because
   * changing anything that would move different data clears it - see
   * changeScope.
   */
  async ensureEstimate(): Promise<void> {
    if (this.estimate || this.busy) {
      return;
    }
    const { sourceTenant, root, targetTenant } = this.choices;
    if (!sourceTenant || !root?.id || !targetTenant) {
      return;
    }
    await this.refreshEstimate();
  }

  async start(): Promise<void> {
    if (!this.readiness.ready) {
      return;
    }

    await this.attempt(async () => {
      this.job = await this.api.startMigration(this.request());
    });
  }

  /** The migration as it will be asked for. */
  request(): MigrationRequest {
    const { sourceTenant, targetTenant, root } = this.choices;
    const request: MigrationRequest = {
      sourceTenant: sourceTenant!,
      rootId: root!.id!,
    };
    if (this.choices.remoteTargetId) {
      request.remoteTargetId = this.choices.remoteTargetId;
    } else {
      request.targetTenant = targetTenant!;
    }

    // Only what was chosen: an omitted field means the service's own default,
    // and sending an empty one would be refused.
    //
    Object.assign(request, this.scopeOfRequest());
    if (this.choices.dataTypes) {
      request.dataTypes = this.choices.dataTypes;
    }
    if (this.choices.dateFrom) {
      request.dateFrom = this.choices.dateFrom;
    }
    if (this.choices.dateTo) {
      request.dateTo = this.choices.dateTo;
    }
    switch (this.choices.credentials) {
      case "file":
        request.provisionCredentials = true;
        break;
      case "self":
        request.deviceSelfCredentials = true;
        break;
      case "certificate":
        request.certificateFleet = true;
        break;
    }
    return request;
  }

  /**
   * Which relationships the migration follows, as the API takes it.
   *
   * Its own function because two requests send it - the migration and the
   * credential pre-flight - and they have to agree: a check that walked a
   * different set of relationships would answer about different devices from the
   * ones being migrated.
   *
   * Unticking every relationship means the same thing as "only this object".
   * Sending neither field would let the service apply its own default and follow
   * children somebody had just explicitly unticked.
   */
  private scopeOfRequest(): { childTypes?: string; noChildren?: boolean } {
    if (this.choices.noChildren || !this.choices.childTypes) {
      return { noChildren: true };
    }
    if (this.choices.childTypes !== DEFAULT_CHOICES.childTypes) {
      return { childTypes: this.choices.childTypes };
    }
    return {};
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

/**
 * Whether a refusal is about the start date.
 *
 * The service says what it could not parse, and its message names the date - it
 * is written for exactly this purpose. Narrow on purpose: anything that is not
 * clearly about the date stays where every other failure goes.
 */
function isAboutTheDate(failure: Failure): boolean {
  return !failure.retryable && /\bdate\b/i.test(failure.detail);
}

/** A comma separated list as its parts, ignoring the empty string. */
function split(value: string): string[] {
  return value
    .split(",")
    .map((part) => part.trim())
    .filter((part) => part !== "");
}

export function failureFrom(error: unknown): Failure {
  if (!(error instanceof ApiError)) {
    return {
      title: gettext("Something went wrong"),
      detail: String(error),
      retryable: false,
    };
  }

  const titles: Record<string, string> = {
    unauthenticated: gettext("Sign in again"),
    forbidden: gettext("Not allowed here"),
    busy: gettext("The service is busy"),
    notFound: gettext("Not found"),
    refused: gettext("That cannot be done"),
    failed: gettext("The migration service could not answer"),
  };

  return {
    title: titles[error.kind] ?? gettext("That cannot be done"),
    detail: error.message,
    retryable: error.retryable,
  };
}
