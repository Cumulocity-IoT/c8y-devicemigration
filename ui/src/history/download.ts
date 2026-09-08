/**
 * Saving a body the browser already has, as a file.
 *
 * Not a link to the endpoint, and that is the point for credentials: the service
 * hands them over exactly once and forgets them as it serves them, so a
 * navigation the platform's session might redirect, or that a popup blocker
 * might swallow, would spend that one read on nothing. The bytes are fetched
 * with the session's own client first, and only then turned into a download.
 */

/**
 * The three browser facilities this needs, as a seam.
 *
 * Injected rather than reached for so the behaviour worth pinning down - that
 * the object URL is always revoked - is testable under this project's node test
 * environment. Adding jsdom for one function would be a dependency for the whole
 * suite to carry.
 */
export interface DownloadHost {
  createObjectURL(blob: Blob): string;
  revokeObjectURL(url: string): void;
  anchor(): { href: string; download: string; click(): void };
}

/** The real one. Every member reads its global lazily, so importing is safe. */
export const browserHost: DownloadHost = {
  createObjectURL: blob => URL.createObjectURL(blob),
  revokeObjectURL: url => URL.revokeObjectURL(url),
  anchor: () => document.createElement('a')
};

export function saveTextFile(
  filename: string,
  body: string,
  type = 'text/csv;charset=utf-8',
  host: DownloadHost = browserHost
): void {
  const url = host.createObjectURL(new Blob([body], { type }));
  try {
    const link = host.anchor();
    link.href = url;
    link.download = filename;
    // Not appended to the document: a click on a detached anchor downloads in
    // every browser this runs in, and appending one leaves a node behind if
    // anything between here and the revoke throws.
    link.click();
  } finally {
    // Revoked whatever happened, or the blob is held for the life of the page -
    // and for credentials that means a list of working device passwords staying
    // reachable from a URL long after the download.
    host.revokeObjectURL(url);
  }
}
