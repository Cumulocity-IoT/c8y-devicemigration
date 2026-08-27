import { byId, labelFor, tenantLabel } from './tenants';

describe('naming a tenant', () => {
  it('shows the company name with the id, because both are needed', () => {
    expect(tenantLabel({ id: 't11111', name: 'Werk Berlin', domain: 't11111.example.com' })).toBe(
      'Werk Berlin (t11111)'
    );
  });

  // Without ROLE_TENANT_MANAGEMENT_READ there is no company name, and the host
  // is what is left - which is still more recognisable than an id.
  it('falls back to the host when there is no name', () => {
    expect(tenantLabel({ id: 't11111', domain: 't11111.example.com' })).toBe(
      't11111.example.com (t11111)'
    );
  });

  it('is the id alone when that is all there is', () => {
    expect(tenantLabel({ id: 't11111' })).toBe('t11111');
    expect(tenantLabel(undefined)).toBe('');
  });

  // A job's record outlives a subscription: the list still has to name the
  // tenant it was migrated to.
  it('names an id nobody described as itself', () => {
    const lookup = byId([{ id: 't11111', name: 'Werk Berlin' }]);

    expect(labelFor('t11111', lookup)).toBe('Werk Berlin (t11111)');
    expect(labelFor('t-gone', lookup)).toBe('t-gone');
    expect(labelFor(undefined, lookup)).toBe('');
  });

  it('ignores a described tenant with no id at all', () => {
    expect(byId([{ name: 'nameless' }, { id: 't1' }]).size).toBe(1);
  });
});
