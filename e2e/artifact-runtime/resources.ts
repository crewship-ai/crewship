/** Suite-owned resources are registered as soon as acquisition succeeds. */
export class FixtureResources {
  constructor(private readonly timeout = 4000) {}
  private closers: (() => Promise<unknown>)[] = []
  own(close: () => Promise<unknown>) { this.closers.push(close) }
  async close() {
    const closers = this.closers.splice(0).reverse()
    const results = await Promise.allSettled(closers.map(close => (async () => {
      let timer: ReturnType<typeof setTimeout> | undefined
      try {
        return await Promise.race([Promise.resolve().then(close), new Promise<never>((_resolve, reject) => { timer = setTimeout(() => reject(new Error('Runtime resource cleanup timed out')), this.timeout) })])
      } finally { clearTimeout(timer) }
    })()))
    const errors = results.flatMap(result => result.status === 'rejected' ? [result.reason] : [])
    if (errors.length) throw new AggregateError(errors, 'Runtime fixture cleanup failed')
  }
}
