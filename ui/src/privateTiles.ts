import maplibregl from "maplibre-gl";

export const PRIVATE_TILE_CONCURRENCY = 4;
export const PRIVATE_TILE_MAX_JOBS = 128;
export const PRIVATE_TILE_MAX_BYTES = 5 << 20;
export const PRIVATE_TILE_QUEUE_BUDGET_MS = 60_000;
export const PRIVATE_TILE_SERVICE_BUDGET_MS = 15_000;
const RETRY_DELAYS_MS = [250, 750];
let nextProtocol = 0;

class PrivateTileError extends Error {
  constructor(
    public status: number,
    public url: string,
    message: string,
  ) {
    super(message);
    this.name = "PrivateTileError";
  }
}

type Scope = {
  logical: string;
  template: string;
  prefix: string;
  intent: string;
  invalid?: number;
};
type Result = { data: ArrayBuffer };
type Waiter = {
  resolve: (result: Result) => void;
  reject: (error: unknown) => void;
  signal: AbortSignal;
  abort: () => void;
};
type Job = {
  key: string;
  url: string;
  scope: Scope;
  controller: AbortController;
  waiters: Set<Waiter>;
  attempts: number;
  state: "queued" | "running" | "sleeping" | "finished";
  deadline?: ReturnType<typeof setTimeout>;
  sleep?: ReturnType<typeof setTimeout>;
};

const aborted = () => new DOMException("Private tile request aborted", "AbortError");

// One registration per map; source identities fence both worker messages and late HTTP results.
export class PrivateTiles {
  readonly protocol = `workouts-private-${++nextProtocol}`;
  private scopes = new Map<string, Scope>();
  private jobs = new Map<string, Job>();
  private queue: Job[] = [];
  private active = 0;
  private drain?: ReturnType<typeof setTimeout>;
  private nextScope = 0;
  private closed = false;

  constructor(
    private origin: string,
    private callbacks: { unauthorized: () => void; unavailable: () => void },
  ) {
    const parsed = new URL(origin);
    if (parsed.origin !== origin || !["http:", "https:"].includes(parsed.protocol))
      throw new Error("Invalid private tile origin");
    maplibregl.addProtocol(this.protocol, this.load);
  }

  setSource(sourceId: string, logical: string, replace = false): string {
    if (this.closed) throw aborted();
    const old = this.scopes.get(sourceId);
    if (old?.logical === logical && !replace) return old.template;
    const path = logical.startsWith(this.origin + "/") ? logical.slice(this.origin.length) : "";
    const match =
      /^\/api\/map-selections\/([a-fA-F0-9]{32})\/(route|coverage)-tiles\/([1-9][0-9]*)\/\{z\}\/\{x\}\/\{y\}\.pbf$/.exec(
        path,
      );
    if (!match) throw new Error("Invalid private tile template");
    const intent = `${match[1]}:${match[3]}`;
    const invalid = [...this.scopes.values()].find(
      (scope) => scope.intent === intent && scope.invalid,
    )?.invalid;
    this.removeSource(sourceId);
    const id = ++this.nextScope;
    const template = `${this.protocol}://tiles/${id}${path}`;
    this.scopes.set(sourceId, {
      logical,
      template,
      prefix: template.split("{z}")[0],
      intent,
      invalid,
    });
    return template;
  }

  invalidateSource(sourceId: string, logical?: string) {
    if (this.scopes.get(sourceId)?.logical !== logical) this.removeSource(sourceId);
  }

  removeSource(sourceId: string) {
    const scope = this.scopes.get(sourceId);
    this.scopes.delete(sourceId);
    for (const job of this.jobs.values()) if (job.scope === scope) this.finish(job, aborted());
  }

  cancel() {
    clearTimeout(this.drain);
    this.drain = undefined;
    for (const job of this.jobs.values()) this.finish(job, aborted());
  }

  reset() {
    this.cancel();
    this.scopes.clear();
  }

  close() {
    this.closed = true;
    this.reset();
    maplibregl.removeProtocol(this.protocol);
  }

  load = (request: { url: string }, controller: AbortController): Promise<Result> => {
    if (this.closed || controller.signal.aborted) return Promise.reject(aborted());
    const scope = [...this.scopes.values()].find((candidate) =>
      request.url.startsWith(candidate.prefix),
    );
    const coordinates =
      scope &&
      /^(0|[1-9][0-9]*)\/(0|[1-9][0-9]*)\/(0|[1-9][0-9]*)\.pbf$/.exec(
        request.url.slice(scope.prefix.length),
      );
    if (!scope || !coordinates) return Promise.reject(aborted());
    const [z, x, y] = coordinates.slice(1).map(Number);
    if (z > 22 || x >= 2 ** z || y >= 2 ** z)
      return Promise.reject(
        new PrivateTileError(400, scope.logical, "Invalid private tile coordinates"),
      );
    const url = scope.logical
      .replace("{z}", String(z))
      .replace("{x}", String(x))
      .replace("{y}", String(y));
    if (scope.invalid)
      return Promise.reject(
        new PrivateTileError(
          scope.invalid === 404 ? 410 : scope.invalid,
          url,
          "Private tile scope unavailable",
        ),
      );
    let job = this.jobs.get(request.url);
    if (job && job.waiters.size >= 64)
      return Promise.reject(new PrivateTileError(503, url, "Private tile waiter limit"));
    if (!job) {
      if (this.jobs.size >= PRIVATE_TILE_MAX_JOBS)
        return Promise.reject(new PrivateTileError(503, url, "Private tile queue full"));
      job = {
        key: request.url,
        url,
        scope,
        controller: new AbortController(),
        waiters: new Set(),
        attempts: 0,
        state: "queued",
      };
      this.jobs.set(job.key, job);
      const pending = job;
      job.deadline = setTimeout(
        () =>
          this.finish(
            pending,
            new PrivateTileError(503, url, "Private tile queue deadline exceeded"),
          ),
        PRIVATE_TILE_QUEUE_BUDGET_MS,
      );
      this.queue.push(job);
    }
    const pending = job;
    return new Promise((resolve, reject) => {
      const waiter: Waiter = {
        resolve,
        reject,
        signal: controller.signal,
        abort: () => {
          pending.waiters.delete(waiter);
          waiter.signal.removeEventListener("abort", waiter.abort);
          reject(aborted());
          if (!pending.waiters.size) this.finish(pending, aborted());
        },
      };
      pending.waiters.add(waiter);
      waiter.signal.addEventListener("abort", waiter.abort, { once: true });
      this.pump();
    });
  };

  private finish(job: Job, error?: unknown, data?: ArrayBuffer) {
    if (job.state === "finished") return;
    job.state = "finished";
    clearTimeout(job.deadline);
    clearTimeout(job.sleep);
    job.controller.abort();
    if (this.jobs.get(job.key) === job) this.jobs.delete(job.key);
    this.queue = this.queue.filter((candidate) => candidate !== job);
    for (const waiter of job.waiters) {
      waiter.signal.removeEventListener("abort", waiter.abort);
      // MapLibre transfers buffers to workers; coalesced consumers need independent ownership.
      if (error !== undefined) waiter.reject(error);
      else waiter.resolve({ data: data!.slice(0) });
    }
    job.waiters.clear();
  }

  private pump() {
    if (this.drain !== undefined) return;
    while (!this.closed && this.active < PRIVATE_TILE_CONCURRENCY && this.queue.length) {
      const job = this.queue.shift()!;
      if (job.state !== "queued") continue;
      if (job.attempts === 0) {
        clearTimeout(job.deadline);
        // Initial backpressure is separate; all retries share the first dispatch's budget.
        job.deadline = setTimeout(
          () =>
            this.finish(
              job,
              new PrivateTileError(503, job.url, "Private tile service deadline exceeded"),
            ),
          PRIVATE_TILE_SERVICE_BUDGET_MS,
        );
      }
      job.state = "running";
      this.active++;
      void this.attempt(job).finally(() => {
        this.active--;
        if (!this.closed && this.queue.length && this.drain === undefined) {
          // Worker cancellation messages arrive as separate tasks after a camera change.
          this.drain = setTimeout(() => {
            this.drain = undefined;
            this.pump();
          }, 0);
        }
      });
    }
  }

  private async attempt(job: Job) {
    let retryAfter = 0;
    try {
      job.attempts++;
      const response = await fetch(job.url, {
        credentials: "same-origin",
        mode: "same-origin",
        cache: "no-store",
        redirect: "error",
        signal: job.controller.signal,
      });
      if (job.controller.signal.aborted) throw aborted();
      if (response.status !== 200 && response.status !== 204) {
        void response.body?.cancel().catch(() => undefined);
        if (response.status === 401 || response.status === 404) {
          this.finish(
            job,
            new PrivateTileError(
              response.status === 404 ? 410 : 401,
              job.url,
              "Private tile scope unavailable",
            ),
          );
          for (const scope of this.scopes.values())
            if (scope.intent === job.scope.intent) scope.invalid = response.status;
          this.cancel();
          if (response.status === 401) this.callbacks.unauthorized();
          else this.callbacks.unavailable();
          return;
        }
        if (response.status === 503) {
          const value = response.headers.get("Retry-After");
          if (value)
            retryAfter =
              Math.min(
                1000,
                Math.max(
                  0,
                  /^\d+(?:\.\d+)?$/.test(value)
                    ? Number(value) * 1000
                    : Date.parse(value) - Date.now(),
                ),
              ) || 0;
        }
        throw new PrivateTileError(response.status, job.url, "Private tile HTTP failure");
      }
      const contentType = response.headers.get("Content-Type")?.split(";")[0].trim().toLowerCase();
      if (
        response.status === 200 &&
        ![
          "application/vnd.mapbox-vector-tile",
          "application/x-protobuf",
          "application/octet-stream",
        ].includes(contentType ?? "")
      ) {
        void response.body?.cancel().catch(() => undefined);
        throw new PrivateTileError(422, job.url, "Invalid private tile content type");
      }
      if (Number(response.headers.get("Content-Length")) > PRIVATE_TILE_MAX_BYTES) {
        void response.body?.cancel().catch(() => undefined);
        throw new PrivateTileError(413, job.url, "Private tile body too large");
      }
      const chunks: Uint8Array[] = [];
      let size = 0;
      const reader = response.body?.getReader();
      try {
        if (reader)
          while (true) {
            const { value, done } = await reader.read();
            if (job.controller.signal.aborted) throw aborted();
            if (done) break;
            size += value.byteLength;
            if (size > PRIVATE_TILE_MAX_BYTES)
              throw new PrivateTileError(413, job.url, "Private tile body too large");
            chunks.push(value);
          }
      } catch (error) {
        await reader?.cancel().catch(() => undefined);
        throw error;
      } finally {
        reader?.releaseLock();
      }
      if (job.controller.signal.aborted) throw aborted();
      const bytes = new Uint8Array(size);
      let offset = 0;
      for (const chunk of chunks) {
        bytes.set(chunk, offset);
        offset += chunk.byteLength;
      }
      this.finish(job, undefined, bytes.buffer);
    } catch (error) {
      if (job.state === "finished") return;
      const status = error instanceof PrivateTileError ? error.status : 0;
      if (
        !job.controller.signal.aborted &&
        [0, 502, 503, 504].includes(status) &&
        job.attempts <= RETRY_DELAYS_MS.length
      ) {
        job.state = "sleeping";
        const delay = Math.max(
          retryAfter,
          RETRY_DELAYS_MS[job.attempts - 1] * (0.8 + Math.random() * 0.4),
        );
        job.sleep = setTimeout(() => {
          job.sleep = undefined;
          job.state = "queued";
          this.queue.push(job);
          this.pump();
        }, delay);
      } else {
        this.finish(job, job.controller.signal.aborted ? aborted() : error);
      }
    }
  }
}
