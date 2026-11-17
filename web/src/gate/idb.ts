// The gate phone's IndexedDB: the outbox, the server ids of sent photos, and the cached lists.

const NAME = "depot-gate";
let opened: Promise<IDBDatabase> | undefined;

function open(): Promise<IDBDatabase> {
  opened ??= new Promise((resolve, reject) => {
    const r = indexedDB.open(NAME, 1);
    r.onupgradeneeded = () => {
      const db = r.result;
      db.createObjectStore("outbox", { keyPath: "seq", autoIncrement: true }); // seq keeps the order of saving
      db.createObjectStore("photoIds"); // outbox photo entry id → the server's photo id
      db.createObjectStore("kv");
    };
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => {
      opened = undefined;
      reject(r.error);
    };
  });
  return opened;
}

/** Runs fn in one transaction and resolves with its request's result once the transaction commits. */
export async function run<T = undefined>(
  stores: string | string[],
  mode: IDBTransactionMode,
  fn: (t: IDBTransaction) => IDBRequest<T> | void,
): Promise<T> {
  const db = await open();
  return new Promise((resolve, reject) => {
    const t = db.transaction(stores, mode);
    const req = fn(t);
    t.oncomplete = () => resolve(req ? req.result : (undefined as T));
    t.onerror = () => reject(t.error);
    t.onabort = () => reject(t.error);
  });
}
