// TypeORM entities: column types come from design:type metadata
// (emitDecoratorMetadata in tsconfig.json).
import { Column, CreateDateColumn, Entity, ManyToOne, OneToMany, PrimaryGeneratedColumn } from "typeorm";

@Entity()
export class Author {
  @PrimaryGeneratedColumn()
  id!: number;

  @Column()
  name!: string;

  @Column({ nullable: true })
  bio?: string;

  @OneToMany(() => Book, (book) => book.author)
  books!: Book[];
}

@Entity()
export class Book {
  @PrimaryGeneratedColumn()
  id!: number;

  @Column()
  title!: string;

  @Column()
  pages!: number;

  @Column()
  available!: boolean;

  @CreateDateColumn()
  createdAt!: Date;

  @ManyToOne(() => Author, (author) => author.books)
  author!: Author;
}
