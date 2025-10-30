// Simple client-side router for single-page navigation
export class Router {
  constructor() {
    this.routes = new Map();
    this.currentPage = null;
  }

  // Register a route with its page element and navigation element
  register(name, pageElement, navElement, onActivate = null) {
    this.routes.set(name, {
      page: pageElement,
      nav: navElement,
      onActivate,
    });
  }

  // Navigate to a specific page
  navigate(name) {
    if (!this.routes.has(name)) {
      console.error(`Route ${name} not found`);
      return;
    }

    if (this.currentPage === name) {
      return;
    }

    const previousName = this.currentPage;
    const previousRoute = previousName ? this.routes.get(previousName) : null;

    const route = this.routes.get(name);
    if (!route) return;

    // Deactivate previous route (if any)
    if (previousRoute) {
      if (previousRoute.page) {
        previousRoute.page.classList.remove("active");
        previousRoute.page.setAttribute("hidden", "true");
        previousRoute.page.removeAttribute("aria-current");
      }
      if (previousRoute.nav) {
        previousRoute.nav.classList.remove("active");
        previousRoute.nav.removeAttribute("aria-current");
      }
      previousRoute.page?.dispatchEvent(
        new CustomEvent("page:deactivate", { detail: { route: previousName } })
      );
    }

    // Deactivate all other pages to guard against stray active states
    this.routes.forEach((other, otherName) => {
      if (otherName === name || otherName === previousName) return;
      if (other.page) {
        other.page.classList.remove("active");
        other.page.setAttribute("hidden", "true");
        other.page.removeAttribute("aria-current");
      }
      if (other.nav) {
        other.nav.classList.remove("active");
        other.nav.removeAttribute("aria-current");
      }
    });

    // Activate the target route
    if (route.page) {
      route.page.classList.add("active");
      route.page.removeAttribute("hidden");
      route.page.setAttribute("aria-current", "true");
      route.page.dispatchEvent(
        new CustomEvent("page:activate", { detail: { route: name } })
      );
    }
    if (route.nav) {
      route.nav.classList.add("active");
      route.nav.setAttribute("aria-current", "page");
    }

    // Call activation callback if provided
    if (route.onActivate) {
      route.onActivate();
    }

    this.currentPage = name;
  }

  // Get current page name
  getCurrentPage() {
    return this.currentPage;
  }

  // Set up click handlers for navigation elements
  setupNavigation() {
    this.routes.forEach((route, name) => {
      if (route.nav) {
        route.nav.addEventListener("click", (e) => {
          e.preventDefault();
          this.navigate(name);
        });
      }
    });
  }
}

// Global router instance
export const router = new Router();
