import { saveTextFile, type DownloadHost } from './download';

/**
 * The one-chance download.
 *
 * The credentials endpoint hands its file over once and forgets it, so the two
 * things worth pinning down are that the body already in hand is what gets
 * saved, and that the object URL is always revoked - a blob left alive keeps a
 * list of working device passwords reachable from a URL for the life of the
 * page.
 */
function recordingHost(clickThrows?: Error) {
  const created: string[] = [];
  const revoked: string[] = [];
  const clicks: { href: string; download: string }[] = [];
  const bodies: Blob[] = [];

  let counter = 0;
  const host: DownloadHost = {
    createObjectURL: blob => {
      bodies.push(blob);
      const url = `blob:test/${++counter}`;
      created.push(url);
      return url;
    },
    revokeObjectURL: url => void revoked.push(url),
    anchor: () => {
      const link = {
        href: '',
        download: '',
        click: () => {
          if (clickThrows) {
            throw clickThrows;
          }
          clicks.push({ href: link.href, download: link.download });
        }
      };
      return link;
    }
  };
  return { host, created, revoked, clicks, bodies };
}

describe('saving a file the browser already has', () => {
  it('downloads the body under the given name', async () => {
    const h = recordingHost();

    saveTextFile('credentials-job-1.csv', 'EXTERNAL_ID,TENANT\nA,t1\n', undefined, h.host);

    expect(h.clicks).toEqual([{ href: h.created[0], download: 'credentials-job-1.csv' }]);
    expect(await h.bodies[0].text()).toBe('EXTERNAL_ID,TENANT\nA,t1\n');
    expect(h.bodies[0].type).toContain('text/csv');
  });

  it('revokes the object URL', () => {
    const h = recordingHost();

    saveTextFile('credentials.csv', 'body', undefined, h.host);

    expect(h.revoked).toEqual(h.created);
  });

  // If the click throws - a blocker, a browser quirk - the blob must still not
  // be left alive: it holds the passwords, and nothing else will come back to
  // free it.
  it('revokes the object URL even when the click fails', () => {
    const h = recordingHost(new Error('blocked'));

    expect(() => saveTextFile('credentials.csv', 'body', undefined, h.host)).toThrow('blocked');
    expect(h.revoked).toEqual(h.created);
  });
});
