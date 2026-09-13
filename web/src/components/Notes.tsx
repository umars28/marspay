import type { ReactNode } from "react";

export type NoteCard = { icon: string; title: string; body: ReactNode };

export function Notes({ cards }: { cards: NoteCard[] }) {
  return (
    <div className="notes">
      {cards.map((card) => (
        <div className="notecard" key={card.title}>
          <h4>
            <svg width="15" height="15">
              <use href={`#${card.icon}`} />
            </svg>{" "}
            {card.title}
          </h4>
          {card.body}
        </div>
      ))}
    </div>
  );
}
