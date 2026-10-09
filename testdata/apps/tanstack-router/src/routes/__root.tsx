import { Link, Outlet, createRootRoute } from "@tanstack/react-router";

export const Route = createRootRoute({
  component: () => (
    <>
      <nav>
        <Link to="/" id="to-home">home</Link> · <Link to="/about" id="to-about">about</Link> ·{" "}
        <Link to="/posts/$postId" params={{ postId: "7" }} id="to-post">post 7</Link>
      </nav>
      <Outlet />
    </>
  ),
});
