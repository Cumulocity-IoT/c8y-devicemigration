import { certificateProblem, certificateRegistration } from './certificate';

const PEM =
  '-----BEGIN CERTIFICATE-----\nMIIexamplebodybase64==\n-----END CERTIFICATE-----';

describe('a pasted migration certificate', () => {
  it('is accepted when it carries the certificate armor', () => {
    expect(certificateProblem(PEM)).toBeUndefined();
    expect(certificateProblem('  ' + PEM + '\n')).toBeUndefined();
  });

  it('asks for something when nothing was pasted', () => {
    expect(certificateProblem('')).toMatch(/Paste the certificate/);
    expect(certificateProblem('   ')).toMatch(/Paste the certificate/);
  });

  it('rejects a base64 blob with the armor stripped, which is the common mistake', () => {
    expect(certificateProblem('MIIexamplebodybase64==')).toMatch(/does not look like a certificate/);
  });
});

describe('the registration request', () => {
  it('sends the full PEM, auto-registration on, enabled — what the platform uploader sends', () => {
    const request = certificateRegistration(PEM, 'Werk Berlin migration');

    expect(request).toEqual({
      name: 'Werk Berlin migration',
      certInPemFormat: PEM,
      autoRegistrationEnabled: true,
      status: 'ENABLED'
    });
  });

  it('names it recognisably when no name is given, rather than sending a blank', () => {
    expect(certificateRegistration(PEM, '   ').name).toBe('Device migration certificate');
  });

  it('trims the pasted certificate', () => {
    expect(certificateRegistration('\n  ' + PEM + '  \n', 'x').certInPemFormat).toBe(PEM);
  });
});
