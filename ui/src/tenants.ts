import type { components } from "./api/types";

/**
 * Naming a tenant.
 *
 * `t87654321` identifies a tenant and describes nothing. What somebody choosing
 * between two of them reads is the company name; what tells two similarly named
 * ones apart is the host. The service supplies whichever it could read - the name
 * needs a role the tenant may not have granted, the host needs only the tenant's
 * own service user - so this takes the best available and always keeps the id,
 * because the id is what every request is made with.
 */
export type TenantDescription = components["schemas"]["TenantDescription"];

/** Tenants by id, for labelling something that only recorded an id. */
export type TenantsById = Map<string, TenantDescription>;

/** What to call a tenant on screen. */
export function tenantLabel(tenant: TenantDescription | undefined): string {
  if (!tenant?.id) {
    return "";
  }
  const known = tenant.name || tenant.domain;
  return known ? `${known} (${tenant.id})` : tenant.id;
}

/**
 * What to call the tenant with this id, from what is known of them.
 *
 * An id nobody described comes back as itself: a job's record outlives a
 * subscription, so the list has to be able to name a tenant that is no longer
 * there.
 */
export function labelFor(id: string | undefined, tenants: TenantsById): string {
  if (!id) {
    return "";
  }
  return tenantLabel(tenants.get(id)) || id;
}

/** The service's answer, as a lookup. */
export function byId(tenants: TenantDescription[] | undefined): TenantsById {
  const lookup: TenantsById = new Map();
  for (const tenant of tenants ?? []) {
    if (tenant.id) {
      lookup.set(tenant.id, tenant);
    }
  }
  return lookup;
}
