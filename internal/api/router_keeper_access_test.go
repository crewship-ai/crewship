package api

// keeperHandlerForTest exposes the credential handler the router built, so a
// test can assert what production wiring handed it. Test-only by placement: the
// handler is an implementation detail of routing and nothing outside should
// reach for it.
func (r *Router) keeperHandlerForTest() *KeeperHandler { return r.keeperHandler }
