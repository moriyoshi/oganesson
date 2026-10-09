// A module worker, bundled separately (new Worker(new URL(...))).
self.onmessage = (e: MessageEvent<number>) => {
  self.postMessage(e.data * 2);
};
