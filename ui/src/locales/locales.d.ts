/**
 * The catalogues are imported for their side effect: the build's webpack step
 * reads every .po file that is part of the module graph and emits one
 * <language>.json per Language header, which is what the platform fetches from
 * /apps/<contextPath>/<language>.json at runtime.
 *
 * They export nothing, hence the declaration - without it TypeScript refuses to
 * import a file it has no idea about.
 */
declare module '*.po' {
  const content: string;
  export default content;
}
