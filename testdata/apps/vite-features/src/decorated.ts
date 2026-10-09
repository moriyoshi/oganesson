// Legacy decorators (experimentalDecorators in tsconfig.json) get the class
// alone; standard decorators would get (value, context).
function tag(label: string) {
  return function <T extends { new (...args: any[]): {} }>(...args: [T, ...unknown[]]) {
    const [ctor] = args;
    return class extends ctor {
      label = `${label}/${args.length}`;
    };
  };
}

class Base {
  stored = "";
  set name(v: string) {
    this.stored = v.toUpperCase();
  }
  get name() {
    return this.stored;
  }
}

@tag("decorated")
export class Thing extends Base {
  // useDefineForClassFields: false: no own "name" field, so the assignment
  // in the constructor reaches Base's setter.
  declare stored: string;
  name!: string;
  constructor() {
    super();
    this.name = "thing";
  }
}
